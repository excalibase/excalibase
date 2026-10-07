import { assertEquals } from "https://deno.land/std@0.224.0/assert/mod.ts";
import { outboundDeniedMessage } from "../runtime/outbound_denied.ts";

Deno.test("Deno's --allow-net refusal is told as the project's outbound host setting", () => {
  assertEquals(
    outboundDeniedMessage('Requires net access to "api.stripe.com:443", run again with the --allow-net flag'),
    "outbound host api.stripe.com:443 isn't allowed for this project; add it in Studio → Functions → Outbound hosts",
  );
});

Deno.test("any other message is left alone", () => {
  assertEquals(outboundDeniedMessage('Requires read access to "/etc/passwd", run again with the --allow-read flag'), null);
  assertEquals(outboundDeniedMessage("connection refused"), null);
});
