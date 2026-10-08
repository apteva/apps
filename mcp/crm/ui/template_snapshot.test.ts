import { expect, test } from "bun:test";
import { templateMessageDisplay } from "./template_snapshot";

test("shows immutable sent content and template metadata", () => {
  const result = templateMessageDisplay("(template #46)", { template_id:46, name:"reengage", availability:"complete", content_source:"send_time_template", body_text:"Hi Roxy, this is John." });
  expect(result.body).toBe("Hi Roxy, this is John.");
  expect(result.label).toBe("Template · reengage (#46)");
  expect(result.notice).toBe("Content captured when sending");
});

test("provider-confirmed content is identified and remains plain text", () => {
  const result = templateMessageDisplay("", {availability:"complete",content_source:"provider_response",body_text:"<script>literal text</script>"});
  expect(result.body).toBe("<script>literal text</script>");
  expect(result.notice).toBe("Content confirmed by provider");
});

test("never presents missing values or HTML as exact sent text", () => {
  for (const availability of ["incomplete", "unavailable"]) {
    const result = templateMessageDisplay("(template #46)", {availability,content_source:"send_time_template",body_text:"Hi {{1}}",body_html:"<script>bad</script>"});
    expect(result.body).toBe("");
    expect(result.notice).toContain("unavailable");
  }
});

test("older templates are honestly unavailable and regular messages unchanged", () => {
  expect(templateMessageDisplay("(template #46)").notice).toContain("historical message");
  expect(templateMessageDisplay("Normal SMS")).toEqual({body:"Normal SMS",label:"",notice:""});
});
