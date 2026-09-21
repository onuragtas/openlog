// The server's bounds (docs/contracts/mobile-agent.md §5), applied here so a payload is never built larger
// than what will be accepted. Over-long values are truncated rather than dropped: a shortened stack is
// still a lead, an absent one is nothing.

import Foundation

enum Limits {
    static let maxSpansPerRequest = 1000
    static let maxAttributesPerSpan = 64
    static let maxAttrValueBytes = 2048
    static let maxNameBytes = 256
    static let maxMessageBytes = 1024
    static let maxStackBytes = 16384
    static let maxURLBytes = 1024
    static let maxUserIDBytes = 128
    static let maxCustomNameBytes = 128
    static let maxCustomUnitBytes = 32
    static let maxCustomParams = 16
    static let maxCustomParamKeyBytes = 64
}

/// Truncates to at most `count` characters without splitting a grapheme: half a character would make the
/// payload invalid on the way out.
func truncate(_ s: String, _ count: Int) -> String {
    s.count <= count ? s : String(s.prefix(count))
}
