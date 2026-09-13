// One display format for listening ports everywhere in the UI:
//   "tcp 0.0.0.0:80", IPv6 bracketed: "tcp [::]:80".
// Discovered services carry {protocol, address, port}; inventory
// listening_port items are keyed "tcp:0.0.0.0:80" / "tcp:[::]:80".

export interface PortRef {
  protocol?: string;
  address?: string;
  port?: number | string;
}

/** "addr:port" with IPv6 addresses bracketed; "*" when the address is empty. */
export function formatHostPort(address: string | undefined, port: number | string | undefined): string {
  let host = (address ?? "").trim();
  if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
  if (host === "") host = "*";
  else if (host.includes(":")) host = `[${host}]`;
  return port === undefined || port === "" ? host : `${host}:${port}`;
}

export function formatPort(p: PortRef): string {
  const hp = formatHostPort(p.address, p.port);
  return p.protocol ? `${p.protocol} ${hp}` : hp;
}

/**
 * Parses an inventory listening_port key ("tcp:0.0.0.0:80", "tcp:[::]:80",
 * also tolerates an unbracketed IPv6 "udp:::1:53"). Returns null if the key
 * does not look like a port key.
 */
export function parsePortKey(key: string): Required<PortRef> | null {
  const m = /^([a-z0-9]+):(.*):(\d+)$/i.exec(key.trim());
  if (!m) return null;
  let address = m[2]!;
  if (address.startsWith("[") && address.endsWith("]")) address = address.slice(1, -1);
  return { protocol: m[1]!, address, port: Number(m[3]) };
}

/** Display form of a listening_port key; the key itself when it can't be parsed. */
export function formatPortKey(key: string): string {
  const p = parsePortKey(key);
  return p ? formatPort(p) : key;
}
