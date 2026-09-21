// The server's bounds (docs/contracts/mobile-agent.md §5), applied here so a payload is never built larger
// than what will be accepted. Over-long values are truncated rather than dropped: a shortened stack is
// still a lead, an absent one is nothing.

const int maxSpansPerRequest = 1000;
const int maxAttributesPerSpan = 64;
const int maxAttrValueBytes = 2048;
const int maxNameBytes = 256;
const int maxMessageBytes = 1024;
const int maxStackBytes = 16384;
const int maxUrlBytes = 1024;
const int maxUserIdBytes = 128;
const int maxCustomNameBytes = 128;
const int maxCustomUnitBytes = 32;
const int maxCustomParams = 16;
const int maxCustomParamKeyBytes = 64;

/// Truncates to at most [n] UTF-16 code units without splitting a surrogate pair — a half character would
/// make the whole payload invalid JSON on the way out.
String truncate(String s, int n) {
  if (s.length <= n) return s;
  var end = n;
  if (end > 0 && s.codeUnitAt(end - 1) >= 0xD800 && s.codeUnitAt(end - 1) <= 0xDBFF) end--;
  return s.substring(0, end);
}
