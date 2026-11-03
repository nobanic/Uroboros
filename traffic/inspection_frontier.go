package traffic

func (pass *contractInspection) discoverScripts(document ExchangeMeta, normalized []byte, offsets []int) {
	for _, position := range clientScriptReferenceRE.FindAllIndex(normalized, -1) {
		reference := string(normalized[position[0]:position[1]])
		resolved, ok := resolveClientScriptReference(document.URL, reference)
		if !ok || !contractAuthorityMatches(resolved, pass.host, pass.port) {
			continue
		}
		identity := canonicalHTTPURL(resolved)
		if identity == "" {
			continue
		}
		if _, exists := pass.seenReferences[identity]; exists {
			continue
		}
		pass.seenReferences[identity] = struct{}{}
		if len(pass.references) == maxContractReferences {
			pass.result.ReferencesTruncated, pass.result.SearchComplete = true, false
			continue
		}
		pass.references = append(pass.references, ContractWorklistReference{
			URL: resolved, DiscoveredInExchangeID: document.ID, ByteOffset: offsets[position[0]],
		})
	}
}

func (pass *contractInspection) resolveFrontier() error {
	for _, reference := range pass.references {
		id, err := pass.store.capturedExchangeForURL(reference.URL)
		if err != nil {
			return err
		}
		reference.CapturedExchangeID = id
		if id == "" {
			pass.result.PendingFetches = append(pass.result.PendingFetches, reference)
		} else {
			pass.result.CapturedReferences = append(pass.result.CapturedReferences, reference)
		}
	}
	if len(pass.result.PendingFetches) != 0 {
		pass.result.SearchComplete = false
	}
	return nil
}
