// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn

import java.util.ArrayDeque
import kotlinx.serialization.Serializable

@Serializable
internal data class ClientStateReport(
    val backendState: String = "",
    val tailscaleRunning: Boolean = false,
    val vpnEnabled: Boolean = false,
    val networkMode: String = "",
    val tailscalePath: String = "",
    val wifiConnected: Boolean = false,
    val cellularConnected: Boolean = false,
    val internetAvailable: Boolean = false,
    val interfaceName: String = "",
    val tailscaleIps: List<String> = emptyList(),
    val allowedIps: List<String> = emptyList(),
    val advertisedRoutes: List<String> = emptyList(),
    val deviceName: String = "",
    val deviceModel: String = "",
    val os: String = "",
    val osVersion: String = "",
    val health: List<String> = emptyList(),
    val lastError: String = "",
)

@Serializable
internal data class ClientLogEntry(
    val timestamp: String,
    val level: String,
    val component: String,
    val message: String,
)

@Serializable internal data class ClientLogsRequest(val logs: List<ClientLogEntry>)

internal data class BufferedClientLog(
    val id: Long,
    val sessionId: String,
    val entry: ClientLogEntry,
)

/** A small in-memory queue so a disconnected client cannot grow its log storage without bound. */
internal class ClientLogBuffer(
    private val maxEntries: Int = 200,
    private val maxCharacters: Int = 256 * 1024,
) {
  private val lock = Any()
  private val entries = ArrayDeque<BufferedClientLog>()
  private var nextID = 0L
  private var characters = 0

  fun append(sessionId: String, entry: ClientLogEntry): Long {
    val size = entrySize(entry)
    synchronized(lock) {
      if (sessionId.isBlank() || maxEntries <= 0 || maxCharacters <= 0 || size > maxCharacters)
          return -1
      while (entries.isNotEmpty() &&
          (entries.size >= maxEntries || characters + size > maxCharacters)) {
        characters -= entrySize(entries.removeFirst().entry)
      }
      nextID++
      entries.addLast(BufferedClientLog(nextID, sessionId, entry))
      characters += size
      return nextID
    }
  }

  fun snapshot(sessionId: String, limit: Int): List<BufferedClientLog> {
    if (limit <= 0) return emptyList()
    synchronized(lock) {
      return entries.filter { it.sessionId == sessionId }.takeLast(limit)
    }
  }

  fun discardSession(sessionId: String) {
    if (sessionId.isBlank()) return
    synchronized(lock) {
      val retained = ArrayDeque<BufferedClientLog>(entries.size)
      while (entries.isNotEmpty()) {
        val item = entries.removeFirst()
        if (item.sessionId == sessionId) {
          characters -= entrySize(item.entry)
        } else {
          retained.addLast(item)
        }
      }
      entries.addAll(retained)
    }
  }

  fun discardOtherSessions(sessionId: String) {
    if (sessionId.isBlank()) return
    synchronized(lock) {
      val retained = ArrayDeque<BufferedClientLog>(entries.size)
      while (entries.isNotEmpty()) {
        val item = entries.removeFirst()
        if (item.sessionId == sessionId) {
          retained.addLast(item)
        } else {
          characters -= entrySize(item.entry)
        }
      }
      entries.addAll(retained)
    }
  }

  fun acknowledge(ids: Collection<Long>) {
    if (ids.isEmpty()) return
    val acknowledged = ids.toSet()
    synchronized(lock) {
      val retained = ArrayDeque<BufferedClientLog>(entries.size)
      while (entries.isNotEmpty()) {
        val item = entries.removeFirst()
        if (item.id in acknowledged) {
          characters -= entrySize(item.entry)
        } else {
          retained.addLast(item)
        }
      }
      entries.addAll(retained)
    }
  }

  fun size(): Int = synchronized(lock) { entries.size }

  private fun entrySize(entry: ClientLogEntry): Int =
      entry.timestamp.length + entry.level.length + entry.component.length + entry.message.length
}
