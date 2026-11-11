package app.pinky.companion

/**
 * A small streaming JSON writer. Trees are large and built on every request,
 * so this appends straight into one StringBuilder instead of building
 * org.json objects first.
 */
class Json(capacity: Int = 256) {
    private val sb = StringBuilder(capacity)

    // One entry per open container: true until its first member is written.
    private var first = BooleanArray(32)
    private var depth = 0
    private var afterName = false

    fun beginObject(): Json {
        separate()
        sb.append('{')
        push()
        return this
    }

    fun endObject(): Json {
        depth--
        sb.append('}')
        return this
    }

    fun beginArray(): Json {
        separate()
        sb.append('[')
        push()
        return this
    }

    fun endArray(): Json {
        depth--
        sb.append(']')
        return this
    }

    fun name(name: String): Json {
        separate()
        string(name)
        sb.append(':')
        afterName = true
        return this
    }

    fun value(v: CharSequence?): Json {
        separate()
        if (v == null) sb.append("null") else string(v)
        return this
    }

    fun value(v: Long): Json {
        separate()
        sb.append(v)
        return this
    }

    fun value(v: Int): Json = value(v.toLong())

    fun value(v: Boolean): Json {
        separate()
        sb.append(v)
        return this
    }

    fun value(v: Double): Json {
        separate()
        if (v.isNaN() || v.isInfinite()) sb.append("null") else sb.append(v)
        return this
    }

    /** Writes an already encoded JSON value. */
    fun raw(encoded: String): Json {
        separate()
        sb.append(encoded)
        return this
    }

    fun field(name: String, v: CharSequence?): Json = name(name).value(v)
    fun field(name: String, v: Long): Json = name(name).value(v)
    fun field(name: String, v: Int): Json = name(name).value(v)
    fun field(name: String, v: Boolean): Json = name(name).value(v)
    fun field(name: String, v: Double): Json = name(name).value(v)

    /** Writes the string field only when it is not null or empty. */
    fun fieldIfNotEmpty(name: String, v: CharSequence?): Json {
        if (!v.isNullOrEmpty()) field(name, v)
        return this
    }

    /** Writes `"name":true` only when v is true. */
    fun flag(name: String, v: Boolean): Json {
        if (v) field(name, true)
        return this
    }

    fun rect(name: String, left: Int, top: Int, right: Int, bottom: Int): Json {
        name(name)
        separate()
        sb.append('[').append(left).append(',').append(top).append(',')
            .append(right).append(',').append(bottom).append(']')
        return this
    }

    override fun toString(): String = sb.toString()

    private fun push() {
        if (depth == first.size) first = first.copyOf(depth * 2)
        first[depth++] = true
    }

    private fun separate() {
        if (afterName) {
            afterName = false
            return
        }
        if (depth > 0) {
            if (first[depth - 1]) first[depth - 1] = false else sb.append(',')
        }
    }

    private fun string(s: CharSequence) {
        sb.append('"')
        for (i in 0 until s.length) {
            val c = s[i]
            when {
                c == '"' -> sb.append("\\\"")
                c == '\\' -> sb.append("\\\\")
                c == '\n' -> sb.append("\\n")
                c == '\r' -> sb.append("\\r")
                c == '\t' -> sb.append("\\t")
                c < ' ' || c == ' ' || c == ' ' -> unicodeEscape(c)
                Character.isHighSurrogate(c) -> {
                    // Keep valid pairs as they are; escape lone halves so the
                    // UTF-8 encoder never replaces them with '?'.
                    if (i + 1 < s.length && Character.isLowSurrogate(s[i + 1])) sb.append(c) else unicodeEscape(c)
                }
                Character.isLowSurrogate(c) -> {
                    if (i > 0 && Character.isHighSurrogate(s[i - 1])) sb.append(c) else unicodeEscape(c)
                }
                else -> sb.append(c)
            }
        }
        sb.append('"')
    }

    private fun unicodeEscape(c: Char) {
        sb.append("\\u")
        val hex = Integer.toHexString(c.code)
        for (k in hex.length until 4) sb.append('0')
        sb.append(hex)
    }
}
