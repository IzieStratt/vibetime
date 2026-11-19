// Minimal fake of the Gemini API (generateContent / streamGenerateContent / countTokens),
// so the real Gemini CLI can run in tests without an account.
import { createServer } from "node:http";
const port = +process.argv[2];
const reply = { candidates: [{ content: { role: "model", parts: [{ text: "hello" }] }, finishReason: "STOP", index: 0 }],
  usageMetadata: { promptTokenCount: 5, candidatesTokenCount: 1, totalTokenCount: 6 } };
createServer((req, res) => {
  let body = ""; req.on("data", c => body += c); req.on("end", () => {
    if (req.url.includes(":countTokens")) { res.writeHead(200, { "content-type": "application/json" }); return res.end('{"totalTokens":5}'); }
    if (req.url.includes(":streamGenerateContent")) {
      res.writeHead(200, { "content-type": "text/event-stream" });
      return res.end("data: " + JSON.stringify(reply) + "\r\n\r\n");
    }
    if (req.url.includes(":generateContent")) {
      // the CLI also asks for JSON (e.g. next-speaker checks); give a valid, terminal answer
      const json = /responseMimeType|responseSchema|responseJsonSchema/.test(body);
      const r = json ? { ...reply, candidates: [{ ...reply.candidates[0], content: { role: "model", parts: [{ text: '{"next_speaker":"user","reasoning":"done"}' }] } }] } : reply;
      res.writeHead(200, { "content-type": "application/json" }); return res.end(JSON.stringify(r));
    }
    res.writeHead(404); res.end("{}");
  });
}).listen(port, "127.0.0.1");
