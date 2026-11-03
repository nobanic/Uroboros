type InferenceRecord = {
  id: number;
  model: string;
  status: string;
  input_tokens: number;
  output_tokens: number;
  [key: string]: unknown;
};

function prettyRecordJSON(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

/** Raw recorded wire strings remain byte-exact; only formatted JSON shares a serializer. */
export function inferenceRecordDetail(id: number, records: InferenceRecord[]) {
  const item = records.find((r) => r.id === id) ?? records[0];
  return {
    ...item,
    request_body: prettyRecordJSON({
      model: item.model,
      system: "uroboros.lab demonstration record.",
      messages: [
        {
          role: "user",
          content:
            "Demo operator note: review the linked evidence and continue the verified assessment workflow. Context: system.",
        },
      ],
      tools: ["bash", "insert_assets", "record_fact", "report_finding"],
    }),
    response_body: prettyRecordJSON({
      stop_reason: item.status === "error" ? "error" : "tool_use",
      content: [
        { type: "text", text: "Demo assessment update. Context: search, payload." },
        { type: "tool_use", name: "bash", input: { command: "curl -s 'https://www.acme.com/search?q=1%27'" } },
      ],
      usage: { input_tokens: item.input_tokens, output_tokens: item.output_tokens },
    }),
    // Mock fixture data.
    raw_request: JSON.stringify({
      model: item.model,
      max_tokens: 8192,
      stream: true,
      system: [{ type: "text", text: "Demo assessment update.", cache_control: { type: "ephemeral" } }],
      messages: [
        {
          role: "user",
          content:
            "Demo operator note: review the linked evidence and continue the verified assessment workflow. Context: system.",
        },
      ],
      tools: [
        {
          name: "bash",
          description:
            "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
          input_schema: { type: "object", properties: { command: { type: "string" } }, required: ["command"] },
        },
      ],
      thinking: { type: "enabled" },
    }),
    raw_response: [
      `event: message_start`,
      `data: {"type":"message_start","message":{"id":"msg_01mock","model":"${item.model}","usage":{"input_tokens":${item.input_tokens},"output_tokens":1}}}`,
      ``,
      `event: content_block_delta`,
      "uroboros.lab demonstration record. Context: data, type, content_block_delta, index, delta.",
      ``,
      `event: content_block_start`,
      `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01mock","name":"bash"}}`,
      ``,
      `event: content_block_delta`,
      `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\\"command\\":\\"curl -s 'https://www.acme.com/search?q=1%27'\\"}"}}`,
      ``,
      `event: message_delta`,
      `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":${item.output_tokens}}}`,
      ``,
      `event: message_stop`,
      `data: {"type":"message_stop"}`,
      ``,
    ].join("\n"),
  };
}
