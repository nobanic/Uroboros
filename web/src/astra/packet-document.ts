export type PacketSegment = {
  text: string;
  tone?: "protocol" | "method" | "path" | "header" | "string" | "number" | "literal" | "markup";
};
export function packetDocument(raw: string) {
  const lines = (raw || "(empty)").replaceAll("\r\n", "\n").split("\n");
  const separator = lines.indexOf(""),
    body =
      separator < 0
        ? ""
        : lines
            .slice(separator + 1)
            .join("\n")
            .trimStart();
  const format = body.startsWith("{") || body.startsWith("[") ? "json" : body.startsWith("<") ? "markup" : "plain";
  return { lines, separator, format };
}

export function packetSegments(line: string, index: number, separator: number, format: string): PacketSegment[] {
  if (index === 0) {
    const request = /^(\S+)(\s+)(\S+)(\s+)(HTTP\/\S+)$/.exec(line);
    if (request)
      return [
        { text: request[1], tone: "method" },
        { text: request[2] },
        { text: request[3], tone: "path" },
        { text: request[4] },
        { text: request[5], tone: "protocol" },
      ];
    const response = /^(HTTP(?:\/\S+)?)(\s+)(\d{3})(.*)$/.exec(line);
    if (response)
      return [
        { text: response[1], tone: "protocol" },
        { text: response[2] },
        { text: response[3], tone: "number" },
        { text: response[4] },
      ];
  }
  if (index > 0 && (separator < 0 || index < separator)) {
    const colon = line.indexOf(":");
    if (colon > 0) return [{ text: line.slice(0, colon), tone: "header" }, { text: line.slice(colon) }];
  }
  if (separator >= 0 && index > separator && format !== "plain") {
    const pattern =
      format === "json"
        ? /"(?:\\.|[^"\\])*"\s*:?|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g
        : /<!--[\s\S]*?-->|<\/?[A-Za-z][^>]*>/g;
    const segments: PacketSegment[] = [];
    let cursor = 0,
      tokens = 0;
    for (const token of line.matchAll(pattern)) {
      if (++tokens > 1000) break;
      if (token.index > cursor) segments.push({ text: line.slice(cursor, token.index) });
      const text = token[0];
      segments.push({
        text,
        tone:
          format === "markup"
            ? "markup"
            : text.startsWith('"')
              ? text.trimEnd().endsWith(":")
                ? "header"
                : "string"
              : /^(?:true|false|null)$/.test(text)
                ? "literal"
                : "number",
      });
      cursor = token.index + text.length;
    }
    if (cursor < line.length) segments.push({ text: line.slice(cursor) });
    return segments;
  }
  return [{ text: line }];
}
