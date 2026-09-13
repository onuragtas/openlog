import { describe, expect, it } from "vitest";
import { formatHostPort, formatPort, formatPortKey, parsePortKey } from "./ports";

describe("formatPort", () => {
  it("formats IPv4 and brackets IPv6", () => {
    expect(formatPort({ protocol: "tcp", address: "0.0.0.0", port: 80 })).toBe("tcp 0.0.0.0:80");
    expect(formatPort({ protocol: "tcp", address: "::", port: 80 })).toBe("tcp [::]:80");
    expect(formatPort({ protocol: "udp", address: "::1", port: 53 })).toBe("udp [::1]:53");
  });

  it("does not double-bracket and tolerates missing parts", () => {
    expect(formatPort({ protocol: "tcp", address: "[::]", port: 22 })).toBe("tcp [::]:22");
    expect(formatPort({ protocol: "tcp", port: 22 })).toBe("tcp *:22");
    expect(formatPort({ address: "127.0.0.1", port: 6379 })).toBe("127.0.0.1:6379");
    expect(formatHostPort("10.0.0.1", undefined)).toBe("10.0.0.1");
  });
});

describe("inventory port keys", () => {
  it("parses bracketed and plain keys", () => {
    expect(parsePortKey("tcp:0.0.0.0:80")).toEqual({ protocol: "tcp", address: "0.0.0.0", port: 80 });
    expect(parsePortKey("tcp:[::]:80")).toEqual({ protocol: "tcp", address: "::", port: 80 });
    expect(parsePortKey("udp:::1:53")).toEqual({ protocol: "udp", address: "::1", port: 53 });
    expect(parsePortKey("not a port")).toBeNull();
  });

  it("formats keys the same way as service ports", () => {
    expect(formatPortKey("tcp:0.0.0.0:80")).toBe(formatPort({ protocol: "tcp", address: "0.0.0.0", port: 80 }));
    expect(formatPortKey("tcp:[::]:80")).toBe("tcp [::]:80");
    expect(formatPortKey("weird")).toBe("weird");
  });
});
