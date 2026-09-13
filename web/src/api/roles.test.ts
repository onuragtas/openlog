import { describe, expect, it } from "vitest";
import { assignableRoles, atLeast, can } from "./roles";

describe("roles", () => {
  it("mirrors the server permission matrix", () => {
    expect(can("viewer", "license_keys.list")).toBe(false);
    expect(can("viewer", "api_keys.create")).toBe(false);
    expect(can("member", "api_keys.create")).toBe(true);
    expect(can("member", "license_keys.list")).toBe(true);
    expect(can("member", "license_keys.manage")).toBe(false);
    expect(can("admin", "license_keys.manage")).toBe(true);
    expect(can("admin", "invitations.manage")).toBe(true);
    expect(can("owner", "audit.read")).toBe(true);
    expect(can(null, "api_keys.list")).toBe(false);
    expect(atLeast("admin", "member")).toBe(true);
    expect(atLeast("member", "admin")).toBe(false);
  });

  it("only owners grant or change the owner role", () => {
    expect(assignableRoles("owner", "member")).toEqual(["owner", "admin", "member", "viewer"]);
    expect(assignableRoles("owner", "owner")).toEqual(["owner", "admin", "member", "viewer"]);
    expect(assignableRoles("admin", "member")).toEqual(["admin", "member", "viewer"]);
    expect(assignableRoles("admin", "owner")).toEqual([]);
    expect(assignableRoles("member", "viewer")).toEqual([]);
  });
});
