import { describe, expect, test } from "vitest";
import { appOriginFrom } from "./app-origin";

describe("appOriginFrom", () => {
  test("is the origin of APP_BASE_URL", () => {
    expect(appOriginFrom("https://contentfabrique.app")).toBe("https://contentfabrique.app");
    expect(appOriginFrom("https://staging.contentfabrique.app/")).toBe("https://staging.contentfabrique.app");
    expect(appOriginFrom("http://localhost:3000")).toBe("http://localhost:3000");
    expect(appOriginFrom(" https://contentfabrique.app:443 ")).toBe("https://contentfabrique.app");
  });

  test("an unset value is only fine on localhost", () => {
    expect(appOriginFrom(undefined, new URL("http://localhost:3000/x"))).toBe("http://localhost:3000");
    expect(appOriginFrom("", new URL("http://127.0.0.1:3000/"))).toBe("http://127.0.0.1:3000");
    expect(() => appOriginFrom(undefined, new URL("https://contentfabrique.app/"))).toThrow(/APP_BASE_URL/);
    expect(() => appOriginFrom("")).toThrow(/APP_BASE_URL/);
  });

  test("a value that is not a web URL is refused", () => {
    expect(() => appOriginFrom("ftp://contentfabrique.app")).toThrow();
    expect(() => appOriginFrom("contentfabrique.app")).toThrow();
  });
});
