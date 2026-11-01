package io.github.onuragtas.openlog

import java.io.File

// The persistent visitor id behind DAU, MAU and retention (docs/contracts/analytics.md §1).
//
// Unlike a session this recognises someone across launches, so what it may do is narrower than what it looks like:
//
//  - It reaches the disk only once the server said this key is in `visitors` mode. Until then it lives in memory.
//  - A withdrawal outlives the process. Otherwise the next launch would hand someone who just said no a new id.
//  - Without a directory there is no id at all. A new one per launch would report every launch as a new person, which
//    is worse than reporting nothing: DAU and new users would look real and be wrong.

private const val FILE_NAME = "openlog_visitor"
private const val OPT_OUT = "optout"

/** Rotated after 13 months, matching how long the server keeps an actor (analytics.md §1.2). */
internal const val VISITOR_MAX_AGE_MS = 395L * 24 * 60 * 60 * 1000

private val ID_SHAPE = Regex("^[0-9a-f]{32}$")

/**
 * Where this visitor falls in [0, 1): FNV-1a of the id over 2^32. Sampling is once per visitor when there is one
 * (analytics.md §2). Every SDK computes exactly this, so one person is in or out of the sample on all of them.
 */
internal fun visitorRoll(id: String): Double {
    var h = 2166136261L
    for (c in id) {
        h = ((h xor c.code.toLong()) * 16777619L) and 0xffffffffL
    }
    return h / 4294967296.0
}

internal class Visitor(
    enabled: Boolean,
    directory: String?,
    private val now: () -> Long = System::currentTimeMillis,
) {
    private class Stored(val id: String, val createdAt: Long, val optOut: Boolean)

    private val file: File? = directory?.let { File(it, FILE_NAME) }
    private var createdAt = 0L
    private var persistent = false

    /** The id to send, or "" when there is none. */
    @Volatile
    var id: String = ""
        private set

    init {
        // An id on disk survives a default of false: an application that asks first starts every launch disabled and
        // enables after reading its own consent, and the person it asked must not become new each time.
        if (enabled && !read().optOut) adopt()
    }

    private fun read(): Stored {
        try {
            val lines = file?.takeIf { it.exists() }?.readLines() ?: emptyList()
            if (lines.firstOrNull() == OPT_OUT) return Stored("", 0, true)
            if (lines.size >= 2) return Stored(lines[0], lines[1].toLongOrNull() ?: 0, false)
        } catch (_: Exception) {
            // An unreadable file is no visitor, never an error in somebody's application.
        }
        return Stored("", 0, false)
    }

    private fun write(content: String?) {
        val f = file ?: return
        try {
            if (content == null) {
                f.delete()
                return
            }
            // Renamed into place, so a crash mid-write cannot leave half an id behind.
            val tmp = File(f.path + ".tmp")
            tmp.writeText(content)
            if (!tmp.renameTo(f)) tmp.delete()
        } catch (_: Exception) {
            // Without storage the visitor is not remembered, and nothing else changes.
        }
    }

    private fun adopt() {
        if (file == null) return
        val stored = read()
        val at = now()
        val fresh = ID_SHAPE.matches(stored.id) && at - stored.createdAt < VISITOR_MAX_AGE_MS
        createdAt = if (fresh) stored.createdAt else at
        id = if (fresh) stored.id else newSessionId()
    }

    /** The server says this key recognises visitors. */
    @Synchronized
    fun confirm() {
        persistent = true
        if (id.isNotEmpty()) write("$id\n$createdAt\n")
    }

    /** The server says this key does not: nothing stays on the device, except a person's own withdrawal. */
    @Synchronized
    fun discard() {
        persistent = false
        id = ""
        if (!read().optOut) write(null)
    }

    /** Disabling deletes the id, so enabling again starts a new one. */
    @Synchronized
    fun setEnabled(enabled: Boolean) {
        if (!enabled) {
            id = ""
            write("$OPT_OUT\n")
            return
        }
        if (id.isNotEmpty()) return
        val wasOptOut = read().optOut
        adopt()
        if (persistent && id.isNotEmpty()) {
            write("$id\n$createdAt\n")
        } else if (wasOptOut) {
            write(null)
        }
    }
}
