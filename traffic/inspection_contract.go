package traffic

type contractInspectionLimits struct {
	context, documents, bytes, matches int
}

func (limits *contractInspectionLimits) normalize() error {
	for _, bound := range []struct {
		value             *int
		fallback, maximum int
		name              string
	}{
		{&limits.context, 240, maxInspectContext, "context_bytes"},
		{&limits.documents, defaultContractDocuments, maxContractDocuments, "max_documents"},
		{&limits.bytes, defaultContractBytes, maxContractBytes, "max_total_bytes"},
		{&limits.matches, defaultContractMatches, maxContractMatches, "max_matches"},
	} {
		value, err := inspectionBound(*bound.value, bound.fallback, bound.maximum, bound.name)
		if err != nil {
			return err
		}
		*bound.value = value
	}
	return nil
}

type contractInspection struct {
	store          *Traffic
	host, port     string
	limits         contractInspectionLimits
	patterns       []contractPattern
	result         ContractWorklistResult
	remainingBytes int
	references     []ContractWorklistReference
	seenReferences map[string]struct{}
}

func (inspection trafficInspection) worklist(host string, patterns []string, regexMode, caseSensitive bool, context, documents, bytes, matches int) (ContractWorklistResult, error) {
	hostname, port, err := normalizeSearchHost(host)
	if err != nil {
		return ContractWorklistResult{}, err
	}
	limits := contractInspectionLimits{context: context, documents: documents, bytes: bytes, matches: matches}
	if err := limits.normalize(); err != nil {
		return ContractWorklistResult{}, err
	}
	compiled, selected, err := compileContractPatterns(patterns, regexMode, caseSensitive)
	if err != nil {
		return ContractWorklistResult{}, err
	}
	rows, truncated, err := inspection.store.clientArtifactRows(hostname, port, limits.documents+1)
	if err != nil {
		return ContractWorklistResult{}, err
	}
	pass := contractInspection{
		store: inspection.store, host: hostname, port: port, limits: limits, patterns: compiled,
		remainingBytes: limits.bytes, references: []ContractWorklistReference{}, seenReferences: make(map[string]struct{}),
		result: ContractWorklistResult{
			Host: host, Patterns: selected, Regex: regexMode, CaseSensitive: caseSensitive,
			Documents: []ContractWorklistDocument{}, Matches: []ContractWorklistMatch{},
			CapturedReferences: []ContractWorklistReference{}, PendingFetches: []ContractWorklistReference{},
			ByteBudget: int64(limits.bytes), SearchComplete: !truncated,
		},
	}
	for _, document := range pass.documents(rows, truncated) {
		if pass.remainingBytes <= 0 {
			pass.result.DocumentsTruncated = true
			pass.result.SearchComplete = false
			break
		}
		body, err := pass.store.loadInspectBody(document.ID, "response")
		if err != nil {
			pass.skip(document, err.Error())
			continue
		}
		if isBinaryBody(body.ContentType, body.Data) && !looksLikeClientScriptURL(document.URL) {
			pass.skip(document, "response body is binary")
			continue
		}
		pass.scan(document, body)
	}
	if err := pass.resolveFrontier(); err != nil {
		return ContractWorklistResult{}, err
	}
	return pass.result, nil
}

func (pass *contractInspection) documents(rows []ExchangeMeta, truncated bool) []ExchangeMeta {
	seen := make(map[string]bool)
	selected := make([]ExchangeMeta, 0, minInt(pass.limits.documents, len(rows)))
	for _, row := range rows {
		identity := canonicalHTTPURL(row.URL)
		if identity == "" {
			identity = row.URL
		}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		if len(selected) == pass.limits.documents {
			truncated = true
			pass.result.SearchComplete = false
		} else {
			selected = append(selected, row)
		}
	}
	pass.result.DocumentsTruncated = truncated
	return selected
}

func (pass *contractInspection) skip(document ExchangeMeta, reason string) {
	pass.result.Skipped = append(pass.result.Skipped, ContractWorklistSkip{ExchangeID: document.ID, URL: document.URL, Reason: reason})
	pass.result.SearchComplete = false
}

func (pass *contractInspection) scan(document ExchangeMeta, body inspectBody) {
	length := minInt(len(body.Data), pass.remainingBytes)
	raw := body.Data[:length]
	pass.remainingBytes -= length
	out := &pass.result
	out.BytesScanned += int64(length)
	complete := body.Complete && length == len(body.Data)
	out.SearchComplete = out.SearchComplete && complete
	out.Documents = append(out.Documents, ContractWorklistDocument{
		ExchangeID: document.ID, URL: document.URL, ContentType: body.ContentType,
		BodySHA256: body.SHA256, BlobSHA256: body.BlobHash, BodyBytes: body.Total,
		BytesScanned: length, Source: body.Source, SearchComplete: complete,
	})
	out.DocumentsScanned++
	normalized, offsets := normalizeSlashEscapes(raw)
	pass.matchDocument(document, body.SHA256, raw, normalized, offsets)
	pass.discoverScripts(document, normalized, offsets)
}

func (pass *contractInspection) matchDocument(document ExchangeMeta, hash string, raw, normalized []byte, offsets []int) {
	out := &pass.result
	for _, pattern := range pass.patterns {
		remaining := pass.limits.matches - len(out.Matches)
		if remaining <= 0 {
			if pattern.re.FindIndex(normalized) != nil {
				out.MatchesTruncated, out.SearchComplete = true, false
			}
			continue
		}
		positions := pattern.re.FindAllIndex(normalized, remaining+1)
		if len(positions) > remaining {
			out.MatchesTruncated, out.SearchComplete = true, false
			positions = positions[:remaining]
		}
		for _, position := range positions {
			span := inspectionSpan(raw, offsets[position[0]], offsets[position[1]], pass.limits.context, 0)
			out.Matches = append(out.Matches, ContractWorklistMatch{
				ExchangeID: document.ID, URL: document.URL, Pattern: pattern.text, BodySHA256: hash,
				ByteStart: span.ByteStart, ByteEnd: span.ByteEnd, Line: span.Line, ByteColumn: span.ByteColumn,
				Before: span.Before, Match: string(normalized[position[0]:position[1]]), RawMatch: span.Match, After: span.After,
			})
		}
	}
}
