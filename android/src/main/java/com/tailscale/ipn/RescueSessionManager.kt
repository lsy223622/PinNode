// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn

import android.content.Context
import android.content.Intent
import android.net.Network
import android.net.VpnService
import androidx.core.content.edit
import com.tailscale.ipn.ui.localapi.Client
import com.tailscale.ipn.ui.model.Ipn
import com.tailscale.ipn.ui.notifier.Notifier
import com.tailscale.ipn.util.TSLog
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI
import java.net.URL
import java.nio.charset.StandardCharsets
import java.time.Instant
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.filter
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.serializer

data class RescueSessionState(
    val active: Boolean = false,
    val activeRoute: String? = null,
    val networkMode: String = "default",
    val vpnEnabled: Boolean = false,
    val subnetRouterEnabled: Boolean = false,
    val subnetRoutes: List<String> = emptyList(),
    val exitNodeEnabled: Boolean = false,
    val exitNodeSelector: String? = null,
    val advertiseExitNode: Boolean = false,
    val logoutAt: String? = null,
)

/**
 * 协调 PIN 兑换、临时登录、LocalAPI 路由广告和服务端清理。
 *
 * OAuth secret 永远不在这里出现；这里收到的 auth key 只在登录调用链中短暂存在。
 */
class RescueSessionManager(private val app: App) {
  private companion object {
    const val TAG = "RescueSessionManager"
    const val REQUEST_TIMEOUT_MS = 15_000
    const val PREFS_NAME = "pinnode"
    const val KEY_SERVER_URL = "server_url"
    const val KEY_ACTIVE_SESSION = "pinnode_active_session"
    const val KEY_PENDING_CLEANUP = "pinnode_pending_cleanup"
    const val KEY_PENDING_CLEANUPS = "pinnode_pending_cleanups"

    fun netfilterModeValue(value: String): Int? =
        when (value) {
          "" -> null
          "off" -> 0
          "nodivert" -> 1
          "on" -> 2
          else -> throw IOException("服务端下发了无效的 netfilterMode")
        }
  }

  @Serializable
  private data class StartRequest(
      val code: String,
      val gatewayRoute: String = "",
      val wifiSubnetRoute: String = "",
  )

  @Serializable
  private data class ExitPolicy(
      val onAppClose: Boolean = false,
      val networkChange: String = "",
      val afterConfigSeconds: Long = 0,
      val afterLoginSeconds: Long = 0,
      val at: String = "",
  )

  @Serializable
  private data class RescueConfig(
      val networkMode: String = "default",
      val vpnEnabled: Boolean = true,
      val acceptRoutes: Boolean = true,
      val acceptDNS: Boolean = true,
      val useExitNode: Boolean = false,
      val exitNodeId: String = "",
      val exitNodeIp: String = "",
      val autoExitNode: String = "",
      val exitNodeAllowLanAccess: Boolean = false,
      val subnetRouter: Boolean = true,
      val autoGatewayRoute: Boolean = true,
      val autoWiFiSubnetRoute: Boolean = false,
      val advertiseRoutes: List<String> = emptyList(),
      val advertiseExitNode: Boolean = false,
      val disableSNAT: Boolean = false,
      val noStatefulFiltering: Boolean = false,
      val shieldsUp: Boolean = false,
      val runSSHServer: Boolean = false,
      val runWebClient: Boolean = false,
      val postureChecking: Boolean = false,
      val remoteConfig: Boolean = false,
      val hostname: String = "",
      val netfilterMode: String = "",
      val appConnector: Boolean = false,
      val exitPolicy: ExitPolicy = ExitPolicy(),
  )

  @Serializable
  private data class StartResponse(
      val sessionId: String,
      val sessionToken: String,
      val authKey: String,
      val provisioningHostname: String,
      val heartbeatIntervalSeconds: Long = 0,
      val gatewayRoute: String,
      val routes: List<String> = emptyList(),
      val wifiRoutes: List<String> = emptyList(),
      val config: RescueConfig = RescueConfig(),
      val expiresAt: String = "",
  )

  @Serializable private data class AttachDeviceRequest(val nodeId: String)

  @Serializable
  private data class ActiveSession(
      val id: String,
      val token: String,
      val route: String = "",
      val routes: List<String> = emptyList(),
      val wifiRoutes: List<String> = emptyList(),
      val config: RescueConfig = RescueConfig(),
      val expiresAt: String = "",
      val serverUrl: String = "",
      val attached: Boolean = true,
      val heartbeatIntervalSeconds: Long = 0,
  )

  @Serializable
  private data class PendingCleanupQueue(val sessions: List<ActiveSession> = emptyList())

  internal class RescueServerHttpException(val status: Int) : IOException()

  internal class RescueInvalidCodeException : IOException()

  internal class RescueConfigurationException(message: String) : IOException(message)

  internal class RescueVpnPermissionException : IOException()

  private val mutex = Mutex()
  private val cleanupMutex = Mutex()
  private val json = Json { ignoreUnknownKeys = true }
  private val configPrefs = app.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
  private val sessionPrefs = app.getEncryptedPrefs()
  private var activeSession: ActiveSession? = null
  private var timedExitJob: Job? = null
  private var heartbeatJob: Job? = null
  private val _sessionState = MutableStateFlow(RescueSessionState())
  val sessionState: StateFlow<RescueSessionState> = _sessionState.asStateFlow()
  private val _vpnPermissionRequests = MutableSharedFlow<Intent>(extraBufferCapacity = 1)
  val vpnPermissionRequests: SharedFlow<Intent> = _vpnPermissionRequests.asSharedFlow()
  @Volatile private var pendingVpnPermission: CompletableDeferred<Boolean>? = null

  init {
    restoreSession()
    monitorNetworkExitPolicy()
    app.applicationScope.launch { retryPendingCleanup() }
  }

  fun isServerLocked(): Boolean = BuildConfig.PINNODE_SERVER_LOCKED

  fun configuredServerDisplay(): String =
      if (isServerLocked()) BuildConfig.PINNODE_SERVER_NAME.trim() else configuredServerUrl()

  fun configuredServerUrl(): String =
      (if (isServerLocked()) {
            BuildConfig.PINNODE_SERVER_URL
          } else {
            configPrefs.getString(KEY_SERVER_URL, BuildConfig.PINNODE_SERVER_URL).orEmpty()
          })
          .trim()
          .trimEnd('/')

  fun setServerUrl(value: String) {
    if (isServerLocked()) return
    configPrefs.edit { putString(KEY_SERVER_URL, value.trim().trimEnd('/')) }
  }

  fun onVpnPermissionResult(granted: Boolean) {
    pendingVpnPermission?.complete(granted)
  }

  suspend fun start(code: String): Result<String> =
      mutex.withLock {
        if (activeSession != null) {
          return@withLock Result.failure(IllegalStateException("临时会话已经运行"))
        }
        if (!Regex("^[0-9]{6}$").matches(code)) {
          return@withLock Result.failure(RescueInvalidCodeException())
        }
        val route = app.currentWifiGatewayRoute().orEmpty()
        val wifiSubnetRoute = app.currentWifiSubnetRoute().orEmpty()
        val serverUrl = configuredServerUrl()
        serverUrlValidationError(serverUrl)?.let {
          return@withLock Result.failure(it)
        }

        val previousNodeID = Notifier.netmap.value?.SelfNode?.StableID
        var pending: ActiveSession? = null
        try {
          val response =
              postJson<StartResponse, StartRequest>(
                  "v1/sessions",
                  StartRequest(code, route, wifiSubnetRoute),
                  null,
                  serverUrl,
              )
          pending =
              ActiveSession(
                  response.sessionId,
                  response.sessionToken,
                  response.gatewayRoute,
                  response.routes,
                  response.wifiRoutes,
                  response.config,
                  response.expiresAt,
                  serverUrl,
                  false,
                  response.heartbeatIntervalSeconds,
              )
          persistSession(pending)
          app.setRescueRoutes(response.wifiRoutes)
          app.setRescueMode(response.config.networkMode == "cellular")
          loginWithAuthKey(
              response.authKey,
              response.config,
              response.routes,
              response.provisioningHostname,
          )
          val nodeId = waitForNodeID(previousNodeID)
          attachDeviceWithRetry(pending, nodeId)
          applyConfig(Client(app.applicationScope), response.config, response.routes)
          pending = pending.copy(attached = true)
          activeSession = pending
          persistSession(pending)
          applySessionState(pending)
          scheduleTimedExit(pending)
          startHeartbeat(pending)
          Result.success(route)
        } catch (error: Throwable) {
          TSLog.e(TAG, "启动临时会话失败: ${error::class.simpleName}")
          runCatching { clearManagedNode() }
              .onFailure { cleanupError ->
                TSLog.e(TAG, "清理临时 Tailscale 节点失败: ${cleanupError::class.simpleName}")
              }
          app.setRescueRoutes(null)
          app.setRescueMode(false)
          _sessionState.value = RescueSessionState()
          clearPersistedSession()
          pending?.let { session ->
            runCatching { stopRemote(session) }.onFailure { persistPendingCleanup(session) }
          }
          Result.failure(error)
        }
      }

  suspend fun stop(): Result<Unit> = mutex.withLock { stopLocked() }

  fun onAppTerminated() = Unit

  fun shouldExitOnAppClose(): Boolean = activeSession?.config?.exitPolicy?.onAppClose == true

  fun exitForAppCloseBlocking() {
    if (!shouldExitOnAppClose()) return
    runBlocking(Dispatchers.IO) { withTimeoutOrNull(5_000) { exitForAppClose() } }
  }

  fun onVpnRevokedBlocking() {
    runBlocking(Dispatchers.IO) { withTimeoutOrNull(5_000) { stop() } }
  }

  suspend fun exitForAppClose() {
    mutex.withLock {
      val session = activeSession ?: return@withLock
      if (!session.config.exitPolicy.onAppClose) return@withLock

      // Persist the terminal state before the first asynchronous cleanup. Some
      // Android variants kill the process immediately after onTaskRemoved returns.
      persistPendingCleanup(session)
      activeSession = null
      timedExitJob?.cancel()
      timedExitJob = null
      heartbeatJob?.cancel()
      heartbeatJob = null
      clearPersistedSession()
      app.setRescueRoutes(null)
      app.setRescueMode(false)
      _sessionState.value = RescueSessionState()

      runCatching { clearManagedNode() }
          .onFailure { TSLog.e(TAG, "应用关闭时注销本地节点失败: ${it::class.simpleName}") }
    }
    app.applicationScope.launch { retryPendingCleanup() }
  }

  private suspend fun stopLocked(): Result<Unit> {
    val session = activeSession
    if (session == null) {
      app.setRescueRoutes(null)
      app.setRescueMode(false)
      clearPersistedSession()
      _sessionState.value = RescueSessionState()
      return Result.success(Unit)
    }
    persistPendingCleanup(session)
    activeSession = null
    timedExitJob?.cancel()
    timedExitJob = null
    heartbeatJob?.cancel()
    heartbeatJob = null
    clearPersistedSession()
    _sessionState.value = RescueSessionState()
    var firstError: Throwable? = null
    runCatching { clearManagedNode() }.onFailure { firstError = it }
    try {
      app.setRescueRoutes(null)
      app.setRescueMode(false)
    } catch (error: Throwable) {
      if (firstError == null) firstError = error
    }
    runCatching { stopRemote(session) }
        .onSuccess { clearPendingCleanup(session.id) }
        .onFailure {
          persistPendingCleanup(session)
          TSLog.e(TAG, "服务端清理失败: ${it::class.simpleName}")
          if (firstError == null) firstError = it
        }
    if (firstError != null) return Result.failure(firstError!!)
    return Result.success(Unit)
  }

  private suspend fun stopRemote(session: ActiveSession) {
    try {
      postEmpty<Unit>(
          "v1/sessions/${session.id}/stop",
          session.token,
          session.serverUrl.ifBlank { configuredServerUrl() },
      )
    } catch (error: RescueServerHttpException) {
      if (error.status !in setOf(404, 410)) throw error
    }
  }

  private fun restoreSession() {
    val encoded = sessionPrefs.getString(KEY_ACTIVE_SESSION, null) ?: return
    val restored =
        runCatching { json.decodeFromString(ActiveSession.serializer(), encoded) }
            .onFailure { TSLog.e(TAG, "恢复 PinNode 会话失败: ${it::class.simpleName}") }
            .getOrNull()
            ?: run {
              clearPersistedSession()
              return
            }
    if (restored.config.exitPolicy.onAppClose) {
      persistPendingCleanup(restored)
      clearPersistedSession()
      app.setRescueRoutes(null)
      app.setRescueMode(false)
      _sessionState.value = RescueSessionState()
      app.applicationScope.launch {
        runCatching { clearManagedNode() }
            .onFailure { TSLog.e(TAG, "恢复时注销已关闭会话失败: ${it::class.simpleName}") }
      }
      return
    }
    if (!restored.attached) {
      persistPendingCleanup(restored)
      clearPersistedSession()
      app.setRescueRoutes(null)
      app.setRescueMode(false)
      app.applicationScope.launch {
        runCatching { clearManagedNode() }
        retryPendingCleanup()
      }
      return
    }
    activeSession = restored
    app.setRescueRoutes(restored.wifiRoutes)
    app.setRescueMode(restored.config.networkMode == "cellular")
    applySessionState(restored)
    scheduleTimedExit(restored)
    startHeartbeat(restored)
    if (restored.config.vpnEnabled && VpnService.prepare(app) == null) {
      app.startVPN()
    }
  }

  private fun persistSession(session: ActiveSession) {
    val encoded = json.encodeToString(ActiveSession.serializer(), session)
    sessionPrefs.edit(commit = true) { putString(KEY_ACTIVE_SESSION, encoded) }
  }

  private fun clearPersistedSession() {
    sessionPrefs.edit(commit = true) { remove(KEY_ACTIVE_SESSION) }
  }

  private fun persistPendingCleanup(session: ActiveSession) {
    synchronized(sessionPrefs) {
      val sessions = loadPendingCleanups().filterNot { it.id == session.id } + session
      val encoded =
          json.encodeToString(PendingCleanupQueue.serializer(), PendingCleanupQueue(sessions))
      sessionPrefs.edit(commit = true) {
        putString(KEY_PENDING_CLEANUPS, encoded)
        remove(KEY_PENDING_CLEANUP)
      }
    }
  }

  private fun clearPendingCleanup(id: String) {
    synchronized(sessionPrefs) {
      val sessions = loadPendingCleanups().filterNot { it.id == id }
      sessionPrefs.edit(commit = true) {
        if (sessions.isEmpty()) {
          remove(KEY_PENDING_CLEANUPS)
        } else {
          putString(
              KEY_PENDING_CLEANUPS,
              json.encodeToString(PendingCleanupQueue.serializer(), PendingCleanupQueue(sessions)))
        }
        remove(KEY_PENDING_CLEANUP)
      }
    }
  }

  private suspend fun retryPendingCleanup() {
    cleanupMutex.withLock {
      for (pending in loadPendingCleanups()) {
        runCatching { stopRemote(pending) }.onSuccess { clearPendingCleanup(pending.id) }
      }
    }
  }

  private fun loadPendingCleanups(): List<ActiveSession> {
    val queue = sessionPrefs.getString(KEY_PENDING_CLEANUPS, null)
    if (queue != null) {
      return runCatching { json.decodeFromString(PendingCleanupQueue.serializer(), queue).sessions }
          .getOrDefault(emptyList())
    }
    val legacy = sessionPrefs.getString(KEY_PENDING_CLEANUP, null) ?: return emptyList()
    return runCatching { listOf(json.decodeFromString(ActiveSession.serializer(), legacy)) }
        .getOrDefault(emptyList())
  }

  private fun applySessionState(session: ActiveSession) {
    val config = session.config
    _sessionState.value =
        RescueSessionState(
            active = true,
            activeRoute = session.route.ifBlank { null },
            networkMode = config.networkMode,
            vpnEnabled = config.vpnEnabled,
            subnetRouterEnabled = config.subnetRouter,
            subnetRoutes = session.wifiRoutes,
            exitNodeEnabled = config.useExitNode,
            exitNodeSelector =
                listOf(config.exitNodeId, config.exitNodeIp, config.autoExitNode)
                    .firstOrNull(String::isNotBlank),
            advertiseExitNode = config.advertiseExitNode,
            logoutAt = session.expiresAt.ifBlank { null },
        )
  }

  private fun scheduleTimedExit(session: ActiveSession) {
    timedExitJob?.cancel()
    val logoutAt = runCatching { Instant.parse(session.expiresAt) }.getOrNull() ?: return
    timedExitJob =
        app.applicationScope.launch {
          delay((logoutAt.toEpochMilli() - System.currentTimeMillis()).coerceAtLeast(0))
          timedExitJob = null
          stop()
        }
  }

  private fun startHeartbeat(session: ActiveSession) {
    heartbeatJob?.cancel()
    heartbeatJob = null
    if (!session.config.exitPolicy.onAppClose || session.heartbeatIntervalSeconds <= 0) return
    val intervalSeconds = session.heartbeatIntervalSeconds.coerceIn(30, 300)
    heartbeatJob =
        app.applicationScope.launch {
          while (activeSession?.id == session.id) {
            delay(TimeUnit.SECONDS.toMillis(intervalSeconds))
            if (activeSession?.id != session.id) return@launch
            runCatching {
                  postEmpty<Unit>(
                      "v1/sessions/${session.id}/heartbeat",
                      session.token,
                      session.serverUrl.ifBlank { configuredServerUrl() },
                  )
                }
                .onFailure { error ->
                  if (error is RescueServerHttpException && error.status in setOf(404, 409, 410)) {
                    stop()
                    return@launch
                  }
                  TSLog.e(TAG, "PinNode 心跳失败: ${error::class.simpleName}")
                }
          }
        }
  }

  private fun monitorNetworkExitPolicy() {
    app.applicationScope.launch {
      var previous = app.rescueNetworkState().value
      app.rescueNetworkState().collect { current ->
        val policy = activeSession?.config?.exitPolicy?.networkChange.orEmpty()
        val shouldExit =
            when (policy) {
              "any-change" -> previous.wifi != current.wifi || previous.cellular != current.cellular
              "wifi-lost" -> previous.wifi.connected && !current.wifi.connected
              "cellular-lost" -> previous.cellular.connected && !current.cellular.connected
              else -> false
            }
        previous = current
        if (shouldExit) {
          stop()
        } else if (current.wifi.connected || current.cellular.connected) {
          launch { retryPendingCleanup() }
        }
      }
    }
  }

  private suspend fun loginWithAuthKey(
      authKey: String,
      config: RescueConfig,
      routes: List<String>,
      hostname: String,
  ) {
    if (config.vpnEnabled) {
      ensureVpnPermission()
      app.startForegroundForLogin()
    }
    val client = Client(app.applicationScope)
    val prefs =
        await { callback ->
              client.editPrefs(Ipn.MaskedPrefs().apply { LoggedOut = false }, callback)
            }
            .getOrThrow()
    applyConfigToPrefs(prefs, config, routes, hostname)
    await { callback ->
          client.start(Ipn.Options(UpdatePrefs = prefs, AuthKey = authKey), callback)
        }
        .getOrThrow()
    await { callback -> client.startLoginInteractive(callback) }.getOrThrow()
    applyConfig(client, config, routes, hostname)
    if (config.vpnEnabled) {
      app.startVPN()
    }
  }

  private suspend fun ensureVpnPermission() {
    val intent = VpnService.prepare(app) ?: return
    val result = CompletableDeferred<Boolean>()
    pendingVpnPermission = result
    if (!_vpnPermissionRequests.tryEmit(intent)) {
      pendingVpnPermission = null
      throw RescueVpnPermissionException()
    }
    try {
      if (!result.await()) {
        throw RescueVpnPermissionException()
      }
    } finally {
      if (pendingVpnPermission === result) {
        pendingVpnPermission = null
      }
    }
  }

  /** 明确退出或策略触发时撤销路由并注销当前受管 profile。 */
  private suspend fun clearManagedNode() {
    val client = Client(app.applicationScope)
    await { callback ->
          client.editPrefs(
              Ipn.MaskedPrefs().apply {
                WantRunning = false
                AdvertiseRoutes = emptyList()
              },
              callback,
          )
        }
        .getOrThrow()
    await { callback -> client.logout(callback) }.getOrThrow()
  }

  private fun applyConfigToPrefs(
      prefs: Ipn.Prefs,
      config: RescueConfig,
      routes: List<String>,
      hostname: String = config.hostname,
  ) {
    prefs.RouteAll = config.acceptRoutes
    prefs.CorpDNS = config.acceptDNS
    prefs.WantRunning = config.vpnEnabled
    prefs.ExitNodeID = config.exitNodeId.takeIf { config.useExitNode && it.isNotBlank() }
    prefs.ExitNodeIP = config.exitNodeIp.takeIf { config.useExitNode && it.isNotBlank() }
    prefs.AutoExitNode = config.autoExitNode.takeIf { config.useExitNode && it.isNotBlank() }
    prefs.ExitNodeAllowLANAccess = config.exitNodeAllowLanAccess
    prefs.ShieldsUp = config.shieldsUp
    prefs.AdvertiseRoutes = routes
    prefs.RunSSH = config.runSSHServer
    prefs.RunWebClient = config.runWebClient
    prefs.NoSNAT = config.disableSNAT
    prefs.NoStatefulFiltering = config.noStatefulFiltering
    prefs.NetfilterMode = netfilterModeValue(config.netfilterMode)
    prefs.AppConnector = Ipn.AppConnectorPrefs(config.appConnector)
    prefs.PostureChecking = config.postureChecking
    prefs.RemoteConfig = config.remoteConfig
    prefs.HostName = hostname
  }

  private suspend fun applyConfig(
      client: Client,
      config: RescueConfig,
      routes: List<String>,
      hostname: String = config.hostname,
  ) {
    val masked =
        Ipn.MaskedPrefs().apply {
          LoggedOut = false
          WantRunning = config.vpnEnabled
          RouteAll = config.acceptRoutes
          CorpDNS = config.acceptDNS
          ExitNodeID = config.exitNodeId.takeIf { config.useExitNode && it.isNotBlank() }
          ExitNodeIP = config.exitNodeIp.takeIf { config.useExitNode && it.isNotBlank() }
          AutoExitNode = config.autoExitNode.takeIf { config.useExitNode && it.isNotBlank() }
          ExitNodeAllowLANAccess = config.exitNodeAllowLanAccess
          ShieldsUp = config.shieldsUp
          AdvertiseRoutes = routes
          RunSSH = config.runSSHServer
          RunWebClient = config.runWebClient
          NoSNAT = config.disableSNAT
          NoStatefulFiltering = config.noStatefulFiltering
          NetfilterMode = netfilterModeValue(config.netfilterMode)
          AppConnector = Ipn.AppConnectorPrefs(config.appConnector)
          PostureChecking = config.postureChecking
          RemoteConfig = config.remoteConfig
          Hostname = hostname
        }
    await { callback -> client.editPrefs(masked, callback) }.getOrThrow()
  }

  private suspend fun waitForNodeID(previousNodeID: String?): String =
      withTimeout(TimeUnit.SECONDS.toMillis(60)) {
        Notifier.netmap
            .filter {
              val nodeID = it?.SelfNode?.StableID
              nodeID?.isNotBlank() == true && nodeID != previousNodeID
            }
            .first()!!
            .SelfNode
            .StableID
      }

  private suspend fun attachDeviceWithRetry(session: ActiveSession, nodeId: String) {
    withTimeout(TimeUnit.SECONDS.toMillis(60)) {
      while (true) {
        try {
          postJson<Unit, AttachDeviceRequest>(
              "v1/sessions/${session.id}/device",
              AttachDeviceRequest(nodeId),
              session.token,
              session.serverUrl.ifBlank { configuredServerUrl() },
          )
          return@withTimeout
        } catch (error: RescueServerHttpException) {
          if (error.status !in setOf(409, 429, 502)) throw error
          delay(2_000)
        }
      }
    }
  }

  private suspend inline fun <reified T, reified B> postJson(
      path: String,
      body: B,
      token: String?,
      baseUrl: String = configuredServerUrl(),
  ): T =
      withContext(Dispatchers.IO) {
        val operation = diagnosticOperation(path)
        val startedAt = System.nanoTime()
        val connection = openConnection(path, token, baseUrl)
        debugRequest(operation, "opened host=${connection.url.host}")
        try {
          connection.requestMethod = "POST"
          connection.doInput = true
          connection.connectTimeout = REQUEST_TIMEOUT_MS
          connection.readTimeout = REQUEST_TIMEOUT_MS
          if (body != null) {
            connection.doOutput = true
            connection.setRequestProperty("Content-Type", "application/json")
            val requestBody = json.encodeToString(serializer<B>(), body)
            connection.outputStream.use {
              it.write(requestBody.toByteArray(StandardCharsets.UTF_8))
            }
            debugRequest(operation, "request-body-sent elapsedMs=${elapsedMillis(startedAt)}")
          }
          val status = connection.responseCode
          debugRequest(operation, "response status=$status elapsedMs=${elapsedMillis(startedAt)}")
          if (status !in 200..299) {
            throw RescueServerHttpException(status)
          }
          if (T::class == Unit::class) {
            @Suppress("UNCHECKED_CAST") return@withContext Unit as T
          }
          connection.inputStream.use { stream ->
            json.decodeFromString<T>(stream.readBytes().toString(StandardCharsets.UTF_8))
          }
        } catch (error: Throwable) {
          debugRequest(
              operation,
              "failed elapsedMs=${elapsedMillis(startedAt)} error=${error::class.simpleName}",
          )
          throw error
        } finally {
          connection.disconnect()
        }
      }

  private suspend inline fun <reified T> postEmpty(
      path: String,
      token: String?,
      baseUrl: String = configuredServerUrl(),
  ): T =
      withContext(Dispatchers.IO) {
        val operation = diagnosticOperation(path)
        val startedAt = System.nanoTime()
        val connection = openConnection(path, token, baseUrl)
        debugRequest(operation, "opened host=${connection.url.host}")
        try {
          connection.requestMethod = "POST"
          connection.doInput = true
          connection.connectTimeout = REQUEST_TIMEOUT_MS
          connection.readTimeout = REQUEST_TIMEOUT_MS
          val status = connection.responseCode
          debugRequest(operation, "response status=$status elapsedMs=${elapsedMillis(startedAt)}")
          if (status !in 200..299) {
            throw RescueServerHttpException(status)
          }
          @Suppress("UNCHECKED_CAST") return@withContext Unit as T
        } catch (error: Throwable) {
          debugRequest(
              operation,
              "failed elapsedMs=${elapsedMillis(startedAt)} error=${error::class.simpleName}",
          )
          throw error
        } finally {
          connection.disconnect()
        }
      }

  private fun diagnosticOperation(path: String): String =
      when {
        path == "v1/sessions" -> "start"
        path.endsWith("/device") -> "attach"
        path.endsWith("/heartbeat") -> "heartbeat"
        path.endsWith("/stop") -> "stop"
        else -> "unknown"
      }

  private fun elapsedMillis(startedAt: Long): Long =
      TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - startedAt)

  private fun debugRequest(operation: String, detail: String) {
    if (BuildConfig.DEBUG) TSLog.d(TAG, "PinNode HTTP operation=$operation $detail")
  }

  private fun openConnection(path: String, token: String?, base: String): HttpURLConnection {
    serverUrlValidationError(base)?.let { throw it }
    val url = URL("$base/${path.trimStart('/')}")
    val network: Network =
        app.currentRescueServerNetwork(url.host)
            ?: throw IOException("没有可用于 PinNode server 的 Network")
    val connection = network.openConnection(url) as HttpURLConnection
    connection.setRequestProperty("Accept", "application/json")
    token?.let { connection.setRequestProperty("Authorization", "Bearer $it") }
    return connection
  }

  private fun serverUrlValidationError(value: String): IOException? {
    if (value.isBlank()) {
      return RescueConfigurationException(
          "尚未配置 PinNode 配置服务器：请填写服务器 API 地址，或安装由管理员锁定服务器的正式包。")
    }
    val uri =
        try {
          URI(value)
        } catch (error: Exception) {
          return RescueConfigurationException(
              "配置服务器地址格式无效：请填写完整的 https:// 地址。")
        }
    if (uri.scheme !in setOf("http", "https") || uri.host.isNullOrBlank()) {
      return RescueConfigurationException(
          "配置服务器地址格式无效：请填写带主机名的 http(s) 地址。")
    }
    if (uri.scheme == "http" && !BuildConfig.DEBUG) {
      return RescueConfigurationException(
          "正式版只能使用 HTTPS 连接配置服务器：请检查地址和服务器证书。")
    }
    if (uri.rawQuery != null || uri.rawFragment != null || uri.userInfo != null) {
      return RescueConfigurationException(
          "配置服务器地址不应包含查询参数、片段或用户信息：请只填写基础地址。")
    }
    return null
  }

  private suspend fun <T> await(call: (((Result<T>) -> Unit) -> Unit)): Result<T> {
    return kotlinx.coroutines.suspendCancellableCoroutine { continuation ->
      call { result -> continuation.resumeWith(Result.success(result)) }
    }
  }
}
