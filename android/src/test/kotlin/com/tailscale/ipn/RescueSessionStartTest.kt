// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn

import android.content.SharedPreferences
import android.net.Network
import android.net.VpnService
import com.tailscale.ipn.ui.localapi.Client
import com.tailscale.ipn.ui.model.Ipn
import com.tailscale.ipn.ui.model.Netmap
import com.tailscale.ipn.ui.model.Tailcfg
import com.tailscale.ipn.ui.notifier.Notifier
import com.tailscale.ipn.util.TSLog
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.URL
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test
import org.mockito.Mockito.*
import org.mockito.kotlin.any
import org.mockito.kotlin.whenever

class RescueSessionStartTest {
  private val app = mock(App::class.java)
  private val dispatcher = StandardTestDispatcher()
  private val scope = CoroutineScope(SupervisorJob() + dispatcher)
  private val stored = ConcurrentHashMap<String, String>()
  private val server = ServerSocket().apply { bind(InetSocketAddress("127.0.0.1", 0)) }
  private lateinit var serverThread: Thread
  private val handlers =
      Executors.newCachedThreadPool { task -> Thread(task).apply { isDaemon = true } }
  private val originalLogger = TSLog.libtailscaleWrapper
  private var startVpn = true
  private var attachedVpn = true
  private var latestRevision = 2
  private var beforeAttach: () -> Unit = {}
  private var onEditPrefs: (Ipn.MaskedPrefs) -> Unit = {}
  private var deferSyncPrefs = false
  private var pendingSyncPrefs: ((Result<Ipn.Prefs>) -> Unit)? = null
  private var syncStatus: () -> Int = { 200 }
  private var boundRoutes: List<String>? = null
  private var cellularMode = false
  private var vpnRunning = false
  private var commitSucceeds = true
  private var appliedPrefs: Ipn.MaskedPrefs? = null
  private val features =
      "[\"session-sync-v1\",\"client-state-report-v1\",\"client-logs-v1\",\"session-policy-capabilities-v1\"]"

  @Before
  fun setUp() {
    stored["server_url"] = "https://pin.test"
    TSLog.libtailscaleWrapper = mock(TSLog.LibtailscaleWrapper::class.java)
    (Notifier.netmap as MutableStateFlow).value = null
    val prefs = preferences()
    whenever(app.applicationScope).thenReturn(scope)
    whenever(app.getSharedPreferences(any(), any())).thenReturn(prefs)
    whenever(app.getEncryptedPrefs()).thenReturn(prefs)
    whenever(app.rescueNetworkState()).thenReturn(MutableStateFlow(RescueNetworkState()))
    doAnswer {
          boundRoutes = it.getArgument(0)
          null
        }
        .`when`(app)
        .setRescueRoutes(anyOrNullList())
    doAnswer {
          cellularMode = it.getArgument(0)
          null
        }
        .`when`(app)
        .setRescueMode(any())
    doAnswer {
          vpnRunning = true
          null
        }
        .`when`(app)
        .startVPN()
    doAnswer {
          vpnRunning = false
          null
        }
        .`when`(app)
        .stopVPN()
    val network = mock(Network::class.java)
    whenever(app.currentRescueServerNetwork(any())).thenReturn(network)
    whenever(network.openConnection(any())).thenAnswer {
      val original = it.getArgument<URL>(0)
      URL("http://127.0.0.1:${server.localPort}${original.path}").openConnection()
    }
    serverThread =
        Thread {
              while (!server.isClosed) {
                val socket =
                    try {
                      server.accept()
                    } catch (_: java.net.SocketException) {
                      break
                    }
                handlers.execute {
                  socket.use {
                    val reader = it.getInputStream().bufferedReader()
                    val path = reader.readLine().split(" ")[1]
                    var contentLength = 0
                    while (true) {
                      val header = reader.readLine()
                      if (header.isNullOrEmpty()) break
                      if (header.startsWith("Content-Length:", ignoreCase = true)) {
                        contentLength = header.substringAfter(':').trim().toInt()
                      }
                    }
                    repeat(contentLength) { reader.read() }
                    val status = if (path.endsWith("/sync")) syncStatus() else 200
                    val body =
                        when (path) {
                          "/v1/meta" ->
                              """{"apiVersion":"v1","protocolVersion":1,"features":$features}"""
                          "/v1/sessions" ->
                              """{"protocolVersion":1,"serverFeatures":$features,"sessionId":"session-a","sessionToken":"token","authKey":"key","provisioningHostname":"provisioning","configRevision":1,"syncIntervalSeconds":60,"gatewayRoute":"192.168.1.1/32","routes":["192.168.1.1/32"],"wifiRoutes":["192.168.1.1/32"],"config":{"networkMode":"default","vpnEnabled":$startVpn},"expiresAt":null}"""
                          "/v1/sessions/session-a/device" -> {
                            beforeAttach()
                            """{"sessionId":"session-a","nodeId":"node-a","enabled":true,"configRevision":$latestRevision,"gatewayRoute":"192.168.2.1/32","routes":["192.168.2.0/24"],"wifiRoutes":["192.168.2.0/24"],"config":{"networkMode":"cellular","vpnEnabled":$attachedVpn}}"""
                          }
                          "/v1/sessions/session-a/stop" -> """{"status":"stopped"}"""
                          "/v1/sessions/session-a/logs" -> """{"accepted":0}"""
                          "/v1/sessions/session-a/sync" ->
                              """{"protocolVersion":1,"serverFeatures":$features,"status":"active","nextSyncAfterSeconds":60,"desiredConfig":{"revision":3,"gatewayRoute":"192.168.3.1/32","routes":["192.168.3.0/24"],"wifiRoutes":["192.168.3.0/24"],"expiresAt":null,"config":{"networkMode":"cellular","vpnEnabled":true}}}"""
                          else -> error("Unexpected request $path")
                        }
                    val bytes = body.toByteArray()
                    it.getOutputStream()
                        .write(
                            ("HTTP/1.1 $status Test\r\nContent-Type: application/json\r\nContent-Length: ${bytes.size}\r\nConnection: close\r\n\r\n")
                                .toByteArray() + bytes)
                  }
                }
              }
            }
            .apply {
              isDaemon = true
              start()
            }
  }

  @After
  fun tearDown() {
    scope.cancel()
    server.close()
    serverThread.join(1000)
    handlers.shutdownNow()
    (Notifier.netmap as MutableStateFlow).value = null
    TSLog.libtailscaleWrapper = originalLogger
    TSLog.pinNodeLogSink = null
  }

  @Test
  fun attachAppliesLatestRoutesAndNetworkModeBeforePersistingRevision() = withClient {
    val manager = RescueSessionManager(app)
    assertTrue(manager.start("123456").isSuccess)
    assertEquals(listOf("192.168.2.0/24"), boundRoutes)
    assertTrue(cellularMode)
    assertEquals(listOf("192.168.2.0/24"), appliedPrefs?.AdvertiseRoutes)
    assertEquals("2", persistedSession()["configRevision"]?.jsonPrimitive?.content)
  }

  @Test
  fun attachDisablesVpnEnabledByInitialConfig() = withClient {
    attachedVpn = false
    assertTrue(RescueSessionManager(app).start("123456").isSuccess)
    assertFalse(vpnRunning)
    assertEquals(false, appliedPrefs?.WantRunning)
  }

  @Test
  fun attachEnablesVpnDisabledByInitialConfig() = withClient {
    startVpn = false
    assertTrue(RescueSessionManager(app).start("123456").isSuccess)
    assertTrue(vpnRunning)
    assertEquals(true, appliedPrefs?.WantRunning)
  }

  @Test
  fun attachAppliesConfigEvenWhenRevisionHasNotChanged() = withClient {
    latestRevision = 1
    assertTrue(RescueSessionManager(app).start("123456").isSuccess)
    assertTrue(vpnRunning)
    assertEquals(listOf("192.168.2.0/24"), boundRoutes)
    assertEquals("1", persistedSession()["configRevision"]?.jsonPrimitive?.content ?: "1")
  }

  @Test
  fun lifecycleStopWhileAttachingDoesNotReactivateSession() = withClient {
    val manager = RescueSessionManager(app)
    beforeAttach = { manager.requestLifecycleStopForVpnRevoke() }
    assertTrue(manager.start("123456").isFailure)
    assertFalse(manager.sessionState.value.active)
    assertNull(stored["pinnode_active_session"])
    assertNull(boundRoutes)
    assertFalse(cellularMode)
  }

  @Test
  fun lifecycleStopWhileApplyingAttachedPrefsDoesNotReactivateSession() = withClient {
    val manager = RescueSessionManager(app)
    onEditPrefs = { prefs ->
      if (prefs.AdvertiseRoutes == listOf("192.168.2.0/24")) {
        manager.requestLifecycleStopForVpnRevoke()
      }
    }
    assertTrue(manager.start("123456").isFailure)
    assertFalse(manager.sessionState.value.active)
    assertFalse(vpnRunning)
    assertNull(stored["pinnode_active_session"])
    assertNull(boundRoutes)
    assertFalse(cellularMode)
  }

  @Test
  fun failedSessionCommitDoesNotPublishSessionOrStartVpn() = withClient {
    commitSucceeds = false
    val manager = RescueSessionManager(app)
    assertTrue(manager.start("123456").isFailure)
    assertFalse(manager.sessionState.value.active)
    verify(app, never()).startVPN()
  }

  @Test
  fun lifecycleStopWaitsForLateSyncPrefsBeforeCleaningBackend() = withClient {
    val manager = RescueSessionManager(app)
    assertTrue(manager.start("123456").isSuccess)
    deferSyncPrefs = true
    val deadline = System.nanoTime() + java.util.concurrent.TimeUnit.SECONDS.toNanos(5)
    while (pendingSyncPrefs == null && System.nanoTime() < deadline) {
      dispatcher.scheduler.runCurrent()
      delay(5)
    }
    val completePrefs = checkNotNull(pendingSyncPrefs) { "sync did not reach editPrefs" }
    clearInvocations(app)
    manager.requestLifecycleStopForVpnRevoke()
    assertNull(boundRoutes)
    completePrefs(Result.success(Ipn.Prefs()))
    dispatcher.scheduler.runCurrent()
    assertEquals(false, appliedPrefs?.WantRunning)
    assertEquals(emptyList<String>(), appliedPrefs?.AdvertiseRoutes)
    assertNull(stored["pinnode_active_session"])
    assertFalse(vpnRunning)
    verify(app, never()).startVPN()

    // A new start must wait until the old write and its cleanup have completed.
    (Notifier.netmap as MutableStateFlow).value = null
    deferSyncPrefs = false
    assertTrue(manager.start("123456").isSuccess)
    assertTrue(vpnRunning)
  }

  @Test
  fun restoreDoesNotReactivateSessionWithDurableCleanupIntent() = withClient {
    val manager = RescueSessionManager(app)
    assertTrue(manager.start("123456").isSuccess)
    val previousActive = stored.getValue("pinnode_active_session")
    manager.requestLifecycleStopForVpnRevoke()
    // A failed active-record deletion can leave this older record on disk.
    stored["pinnode_active_session"] = previousActive
    clearInvocations(app)
    RescueSessionManager(app)
    verify(app, never()).startVPN()
    assertNull(boundRoutes)
    assertFalse(cellularMode)
  }

  @Test
  fun lateExpiredSyncDoesNotStopNewSession() = withClient {
    val manager = RescueSessionManager(app)
    val requested = CountDownLatch(1)
    val release = CountDownLatch(1)
    val first = AtomicBoolean(true)
    syncStatus = {
      if (first.compareAndSet(true, false)) {
        requested.countDown()
        check(release.await(5, TimeUnit.SECONDS))
        410
      } else 200
    }
    assertTrue(manager.start("123456").isSuccess)
    val syncField =
        RescueSessionManager::class.java.getDeclaredField("syncJob").apply { isAccessible = true }
    val oldSync = syncField.get(manager) as Job
    dispatcher.scheduler.runCurrent()
    try {
      assertTrue(requested.await(5, TimeUnit.SECONDS))
      manager.requestLifecycleStopForVpnRevoke()
      (Notifier.netmap as MutableStateFlow).value = null
      assertTrue(manager.start("123456").isSuccess)
      clearInvocations(app)
    } finally {
      release.countDown()
    }
    val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
    while (!oldSync.isCompleted && System.nanoTime() < deadline) {
      dispatcher.scheduler.runCurrent()
      delay(5)
    }
    assertTrue(oldSync.isCompleted)
    dispatcher.scheduler.runCurrent()
    assertTrue(manager.sessionState.value.active)
    assertNotNull(stored["pinnode_active_session"])
    assertTrue(vpnRunning)
    verify(app, never()).stopVPN()
  }

  private fun persistedSession() =
      Json.parseToJsonElement(stored.getValue("pinnode_active_session")).jsonObject

  private fun withClient(test: suspend () -> Unit) {
    mockStatic(VpnService::class.java).use {
      mockConstruction(Client::class.java) { client, _ ->
            doAnswer {
                  appliedPrefs = it.getArgument(0)
                  onEditPrefs(appliedPrefs!!)
                  val callback = it.getArgument<(Result<Ipn.Prefs>) -> Unit>(1)
                  if (deferSyncPrefs && appliedPrefs?.AdvertiseRoutes == listOf("192.168.3.0/24")) {
                    pendingSyncPrefs = callback
                  } else {
                    callback(Result.success(Ipn.Prefs()))
                  }
                  null
                }
                .`when`(client)
                .editPrefs(any(), any())
            doAnswer {
                  it.getArgument<(Result<Unit>) -> Unit>(1)(Result.success(Unit))
                  null
                }
                .`when`(client)
                .start(any(), any())
            doAnswer {
                  (Notifier.netmap as MutableStateFlow).value =
                      Netmap.NetworkMap(
                          SelfNode = Tailcfg.Node(StableID = "node-a"),
                          Peers = emptyList(),
                          Domain = "",
                          UserProfiles = emptyMap(),
                          TKAEnabled = false)
                  it.getArgument<(Result<Unit>) -> Unit>(0)(Result.success(Unit))
                  null
                }
                .`when`(client)
                .startLoginInteractive(any())
            doAnswer {
                  it.getArgument<(Result<Unit>) -> Unit>(0)(Result.success(Unit))
                  null
                }
                .`when`(client)
                .logout(any())
          }
          .use { runBlocking { test() } }
    }
  }

  private fun anyOrNullList(): List<String>? = org.mockito.kotlin.anyOrNull()

  private fun preferences(): SharedPreferences {
    val prefs = mock(SharedPreferences::class.java)
    val editor = mock(SharedPreferences.Editor::class.java)
    whenever(prefs.getString(any(), org.mockito.kotlin.anyOrNull())).thenAnswer {
      stored[it.getArgument<String>(0)] ?: it.getArgument<String?>(1)
    }
    whenever(prefs.edit()).thenReturn(editor)
    whenever(editor.putString(any(), org.mockito.kotlin.anyOrNull())).thenAnswer {
      val key = it.getArgument<String>(0)
      val value = it.getArgument<String?>(1)
      if (value == null) stored.remove(key) else stored[key] = value
      editor
    }
    whenever(editor.remove(any())).thenAnswer {
      stored.remove(it.getArgument<String>(0))
      editor
    }
    whenever(editor.commit()).thenAnswer { commitSucceeds }
    return prefs
  }
}
