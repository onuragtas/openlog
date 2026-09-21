package io.github.onuragtas.openlog

// A minimal JSON writer.
//
// Hand-written rather than depending on kotlinx-serialization or Jackson, for the same reason the Dart and
// Swift clients use what their platforms already have: this is the one library an application cannot choose
// to drop, so it must not impose a version of another on it. What it writes is four nested objects and a
// list of attributes; what it must get right is escaping, which is the only place a writer this small goes
// wrong — so that is what the tests are about.

internal fun jsonEncode(value: Any?): String = StringBuilder().also { write(it, value) }.toString()

private fun write(sb: StringBuilder, value: Any?) {
    when (value) {
        null -> sb.append("null")
        is String -> writeString(sb, value)
        is Boolean -> sb.append(if (value) "true" else "false")
        is Int, is Long -> sb.append(value.toString())
        is Double, is Float -> {
            val d = (value as Number).toDouble()
            // NaN and infinities are not JSON; sending one would make the whole batch unparseable.
            if (d.isNaN() || d.isInfinite()) sb.append("null") else sb.append(d.toString())
        }
        is Map<*, *> -> {
            sb.append('{')
            var first = true
            for ((k, v) in value) {
                if (!first) sb.append(',')
                first = false
                writeString(sb, k.toString())
                sb.append(':')
                write(sb, v)
            }
            sb.append('}')
        }
        is List<*> -> {
            sb.append('[')
            for ((i, v) in value.withIndex()) {
                if (i > 0) sb.append(',')
                write(sb, v)
            }
            sb.append(']')
        }
        else -> writeString(sb, value.toString())
    }
}

private fun writeString(sb: StringBuilder, s: String) {
    sb.append('"')
    for (c in s) {
        when {
            c == '"' -> sb.append("\\\"")
            c == '\\' -> sb.append("\\\\")
            c == '\n' -> sb.append("\\n")
            c == '\r' -> sb.append("\\r")
            c == '\t' -> sb.append("\\t")
            // Everything below 0x20 must be escaped or the document is invalid, and a control character
            // arriving in an error message is exactly how that happens.
            c < ' ' -> sb.append(String.format("\\u%04x", c.code))
            else -> sb.append(c)
        }
    }
    sb.append('"')
}
