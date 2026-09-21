package io.github.onuragtas.openlog

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

class ConfigTest {
    private fun valid(
        key: String = "olb_1a2b3c4d5e6f708192a3b4c5d6e7f809",
        endpoint: String = "https://ingest.example.com:4318",
        appId: String = "com.example.shop",
        sampleRate: Double = 1.0,
    ) = Options(key = key, endpoint = endpoint, appId = appId, sampleRate = sampleRate)

    @Test
    fun `derives the two URLs without a trailing slash`() {
        val c = resolveConfig(valid(endpoint = "https://ingest.example.com:4318///"))
        assertEquals("https://ingest.example.com:4318/v1/rum", c.rumUrl)
        assertEquals("https://ingest.example.com:4318/v1/rum/config", c.configUrl)
    }

    @Test
    fun `refuses options the server would refuse`() {
        // Thrown rather than logged: an SDK that silently does nothing is found out weeks later.
        assertFailsWith<ConfigException> { resolveConfig(valid(key = "olk_an_ingest_license_key")) }
        assertFailsWith<ConfigException> { resolveConfig(valid(endpoint = "ingest.example.com")) }
        assertFailsWith<ConfigException> { resolveConfig(valid(appId = "   ")) }
        assertFailsWith<ConfigException> { resolveConfig(valid(sampleRate = 0.0)) }
        assertFailsWith<ConfigException> { resolveConfig(valid(sampleRate = 1.5)) }
    }
}
