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
