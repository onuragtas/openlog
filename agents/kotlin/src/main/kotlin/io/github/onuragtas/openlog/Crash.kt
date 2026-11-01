package io.github.onuragtas.openlog

import java.io.File

// Crash capture (docs/contracts/mobile-crash.md §2). An unhandled crash is not something the process lives to send, so
// the handler writes one file and returns, and the next launch delivers it. It never touches the network or the queue:
// the process is going down, and the only thing that must not happen here is hiding the crash.

private const val CRASH_PREFIX = "openlog-crash-"
private const val CRASH_SUFFIX = ".json"

/** A crash loop must not fill the device, and the first crashes of a loop say what the rest would. */
internal const val MAX_CRASH_FILES = 10

internal class CrashStore(directory: String, private val now: () -> Long = System::currentTimeMillis) {
    private val dir = File(directory)

    fun write(id: String, body: String) {
        try {
            dir.mkdirs()
            // Keeps the earliest, and drops this one rather than one of them.
            if (pending().size >= MAX_CRASH_FILES) return
            val name = "$CRASH_PREFIX${now()}-$id$CRASH_SUFFIX"
            // Renamed into place, so a process dying mid-write cannot leave half a report to be sent later.
            val tmp = File(dir, "$name.tmp")
            tmp.writeText(body)
            if (!tmp.renameTo(File(dir, name))) tmp.delete()
        } catch (_: Exception) {
            // Nothing can be done from here, and the crash itself must still go ahead.
        }
    }

    fun pending(): List<File> =
        (dir.listFiles { f -> f.name.startsWith(CRASH_PREFIX) && f.name.endsWith(CRASH_SUFFIX) } ?: emptyArray())
            .sortedBy { it.name }
}

/**
 * Sends what the last launch left behind. A file goes once the server has answered for good, and stays for the next launch
 * when it said to wait or never answered: a crash report is the one thing here not worth losing to a bad connection.
 */
internal fun deliverCrashes(store: CrashStore, send: (String) -> Int) {
    for (file in store.pending()) {
        val status = try {
            send(file.readText())
        } catch (_: Exception) {
            -1
        }
        val final = status in 200..299 || (status in 400..499 && status != 429)
        if (!final) return
        file.delete()
    }
}

/** The session an earlier launch was in. The system's record of a crash names none, so a report is attached to it on a best-effort basis. */
internal class LastSession(val id: String, val version: String) {
    fun write(directory: String) {
        try {
            File(directory).mkdirs()
            File(directory, LAST_SESSION_FILE).writeText("$id\n$version")
        } catch (_: Exception) {
            // Without it a report is only flagged as having no known session.
        }
    }

    companion object {
        private const val LAST_SESSION_FILE = "openlog-last-session"

        fun read(directory: String): LastSession? = try {
            val parts = File(directory, LAST_SESSION_FILE).readText().split("\n")
            if (parts.size == 2) LastSession(parts[0], parts[1]) else null
        } catch (_: Exception) {
            null
        }
    }
}

/** What the operating system recorded about a process that is gone. */
internal class EarlierCrash(val kind: String, val type: String, val message: String, val stack: String, val atMs: Long)

private const val REASON_LOW_MEMORY = 3
private const val REASON_CRASH_NATIVE = 5
private const val REASON_ANR = 6
private const val IMPORTANCE_FOREGROUND = 100
private const val MAX_EXITS = 8
private const val MAX_TOMBSTONE_BYTES = 4 shl 20 // past the backtrace is memory dumps, which a report does not use
private const val MAX_ANR_TRACE_BYTES = 256 * 1024 // the main thread is well inside this
private const val LAST_EXIT_FILE = "openlog-last-exit"

internal fun androidSdkInt(): Int = try {
    Class.forName("android.os.Build\$VERSION").getField("SDK_INT").getInt(null)
} catch (_: Exception) {
    0 // not Android
}

private fun Any.call(name: String): Any? = javaClass.getMethod(name).invoke(this)

/**
 * ApplicationExitInfo, by reflection, so this SDK keeps no Android dependency and the rest of it stays plain JVM. API 30 and up:
 * below that there is nothing to read. A Java crash is left out, because the handler in this process already reported it.
 */
internal fun readAndroidExits(context: Any, directory: String, sdkInt: () -> Int = ::androidSdkInt): List<EarlierCrash> {
    val out = ArrayList<EarlierCrash>()
    try {
        if (sdkInt() < 30) return out
        // The system keeps exits from before this SDK was installed, and those are not ours to report.
        val mark = File(directory, LAST_EXIT_FILE)
        val since = try {
            mark.readText().trim().toLongOrNull()
        } catch (_: Exception) {
            null
        }
        val manager = context.javaClass.getMethod("getSystemService", String::class.java).invoke(context, "activity") ?: return out
        val packageName = context.call("getPackageName") as String
        val exits = manager.javaClass
            .getMethod("getHistoricalProcessExitReasons", String::class.java, Int::class.javaPrimitiveType, Int::class.javaPrimitiveType)
            .invoke(manager, packageName, 0, MAX_EXITS) as List<*>
        var newest = since ?: 0L
        for (info in exits) {
            if (info == null) continue
            val at = info.call("getTimestamp") as Long
            if (at > newest) newest = at
            if (since != null && at > since) exitToCrash(info, at)?.let(out::add)
        }
        File(directory).mkdirs()
        mark.writeText((if (newest > 0) newest else System.currentTimeMillis()).toString())
    } catch (_: Exception) {
        // The system's record is a bonus. Whatever is wrong with reading it must not touch the application.
    }
    return out
}

private fun exitToCrash(info: Any, at: Long): EarlierCrash? {
    val description = info.call("getDescription") as String? ?: ""
    return when (info.call("getReason") as Int) {
        REASON_CRASH_NATIVE -> {
            // Below API 31 there is no tombstone, and what is known is that it happened.
            val t = decodeTombstone(traceBytes(info))
            if (t != null) {
                EarlierCrash("signal", t.signalName.ifEmpty { "NativeCrash" }, t.message(), t.stack, at)
            } else {
                EarlierCrash("signal", "NativeCrash", description.ifEmpty { "Native crash" }, "", at)
            }
        }
        REASON_ANR -> EarlierCrash(
            "anr", "ANR", description.ifEmpty { "Application not responding" },
            anrStack(String(traceBytes(info, MAX_ANR_TRACE_BYTES), Charsets.UTF_8)), at,
        )
        // The system also reclaims applications sitting in the background. That is Android working, not a crash.
        REASON_LOW_MEMORY -> if (info.call("getImportance") == IMPORTANCE_FOREGROUND) {
            EarlierCrash("out_of_memory", "OutOfMemory", description.ifEmpty { "Killed for low memory" }, "", at)
        } else {
            null
        }
        else -> null
    }
}

/**
 * An ANR's dump opens with memory figures and a system event log, and a stack limit would be spent on them. The thread that did
 * not respond is what the report is for, so it starts there.
 */
internal fun anrStack(trace: String): String {
    val i = trace.indexOf("\n\"main\"")
    return if (i < 0) trace else trace.substring(i + 1)
}

private fun traceBytes(info: Any, limit: Int = MAX_TOMBSTONE_BYTES): ByteArray = try {
    (info.call("getTraceInputStream") as java.io.InputStream?)?.use { stream ->
        val out = java.io.ByteArrayOutputStream()
        val buf = ByteArray(16 * 1024)
        while (out.size() < limit) {
            val n = stream.read(buf, 0, minOf(buf.size, limit - out.size()))
            if (n < 0) break
            out.write(buf, 0, n)
        }
        out.toByteArray()
    } ?: ByteArray(0)
} catch (_: Exception) {
    ByteArray(0)
}

internal class Tombstone(val signalName: String, val codeName: String, val abortMessage: String, val stack: String) {
    /** A short line for the crash message: the abort's own words when there are any. */
    fun message(): String = when {
        abortMessage.isNotEmpty() -> abortMessage
        codeName.isNotEmpty() -> "$signalName ($codeName)"
        else -> signalName
    }
}

/** One native frame, as the server reads it (mobile-crash.md §3). An image id and an offset, because that is what symbols are found by. */
internal fun nativeFrame(index: Int, binary: String, imageId: String, offset: Long, function: String): String {
    val name = binary.substringAfterLast('/').replace(' ', '_').ifEmpty { "unknown" }
    val id = if (imageId.isEmpty()) "" else "$imageId "
    val fn = if (function.isEmpty()) "" else " $function"
    return "#%02d %s %s+0x%x%s".format(index, name, id, offset, fn)
}

private class Proto(private val b: ByteArray, private var i: Int, private val end: Int) {
    var field = 0
    var wire = 0

    fun next(): Boolean {
        if (i >= end) return false
        val tag = varint()
        field = (tag ushr 3).toInt()
        wire = (tag and 7).toInt()
        return true
    }

    fun varint(): Long {
        var v = 0L
        var shift = 0
        while (shift < 64) {
            require(i < end)
            val c = b[i++].toInt()
            v = v or ((c and 0x7f).toLong() shl shift)
            if (c and 0x80 == 0) return v
            shift += 7
        }
        throw IllegalArgumentException("varint too long")
    }

    private fun length(): Int {
        val n = varint()
        require(n >= 0 && n <= end - i)
        return n.toInt()
    }

    fun message(): Proto {
        val n = length()
        return Proto(b, i, i + n).also { i += n }
    }

    fun text(): String {
        val n = length()
        return String(b, i, n, Charsets.UTF_8).also { i += n }
    }

    fun skip() {
        when (wire) {
            0 -> varint()
            1 -> advance(8)
            2 -> advance(length())
            5 -> advance(4)
            else -> throw IllegalArgumentException("unknown wire type")
        }
    }

    private fun advance(n: Int) {
        require(n <= end - i)
        i += n
    }
}

/**
 * Android's record of a native crash, a protobuf from API 31 (AOSP tombstone.proto), read by hand for the few fields a report
 * uses so this SDK carries no protobuf library. Null when the bytes are not one.
 */
internal fun decodeTombstone(data: ByteArray): Tombstone? {
    try {
        var tid = 0L
        var signal = ""
        var code = ""
        var abort = ""
        val threads = HashMap<Long, Proto>()
        val r = Proto(data, 0, data.size)
        while (r.next()) {
            when {
                r.field == 6 && r.wire == 0 -> tid = r.varint()
                r.field == 10 && r.wire == 2 -> {
                    val s = r.message()
                    while (s.next()) {
                        when {
                            s.field == 2 && s.wire == 2 -> signal = s.text()
                            s.field == 4 && s.wire == 2 -> code = s.text()
                            else -> s.skip()
                        }
                    }
                }
                r.field == 14 && r.wire == 2 -> abort = r.text()
                r.field == 16 && r.wire == 2 -> {
                    val e = r.message()
                    var key = 0L
                    var value: Proto? = null
                    while (e.next()) {
                        when {
                            e.field == 1 && e.wire == 0 -> key = e.varint()
                            e.field == 2 && e.wire == 2 -> value = e.message()
                            else -> e.skip()
                        }
                    }
                    value?.let { threads[key] = it }
                }
                else -> r.skip()
            }
        }
        val crashed = threads[tid]
        if (signal.isEmpty() && crashed == null) return null
        return Tombstone(signal, code, abort, crashed?.let(::threadStack) ?: "")
    } catch (_: IllegalArgumentException) {
        return null
    }
}

private fun threadStack(thread: Proto): String {
    val lines = StringBuilder()
    var index = 0
    while (thread.next()) {
        if (thread.field != 4 || thread.wire != 2) {
            thread.skip()
            continue
        }
        val frame = thread.message()
        var relPc = 0L
        var function = ""
        var file = ""
        var buildId = ""
        while (frame.next()) {
            when {
                frame.field == 1 && frame.wire == 0 -> relPc = frame.varint()
                frame.field == 4 && frame.wire == 2 -> function = frame.text()
                frame.field == 6 && frame.wire == 2 -> file = frame.text()
                frame.field == 8 && frame.wire == 2 -> buildId = frame.text()
                else -> frame.skip()
            }
        }
        if (lines.length < Limits.MAX_STACK_BYTES) lines.append(nativeFrame(index, file, buildId, relPc, function)).append('\n')
        index++
    }
    return lines.toString()
}
