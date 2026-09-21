package io.github.onuragtas.openlog

// The server's bounds (docs/contracts/mobile-agent.md §5), applied here so a payload is never built larger
// than what will be accepted. Over-long values are truncated rather than dropped: a shortened stack is
// still a lead, an absent one is nothing.

internal object Limits {
    const val MAX_SPANS_PER_REQUEST = 1000
    const val MAX_ATTRIBUTES_PER_SPAN = 64
    const val MAX_ATTR_VALUE_BYTES = 2048
    const val MAX_NAME_BYTES = 256
    const val MAX_MESSAGE_BYTES = 1024
    const val MAX_STACK_BYTES = 16384
    const val MAX_URL_BYTES = 1024
    const val MAX_USER_ID_BYTES = 128
    const val MAX_CUSTOM_NAME_BYTES = 128
    const val MAX_CUSTOM_UNIT_BYTES = 32
    const val MAX_CUSTOM_PARAMS = 16
    const val MAX_CUSTOM_PARAM_KEY_BYTES = 64
}

/** Truncates to at most [n] characters. */
internal fun truncate(s: String, n: Int): String = if (s.length <= n) s else s.substring(0, n)
