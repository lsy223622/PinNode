// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn

import android.annotation.SuppressLint
import android.net.ConnectivityManager
import android.net.IpPrefix
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.ParcelFileDescriptor
import com.tailscale.ipn.util.TSLog
import java.net.Inet4Address
import java.net.InetAddress
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

data class RescueLinkState(
    val connected: Boolean = false,
    val internetAvailable: Boolean = false,
    val interfaceName: String? = null,
)

enum class RescueTailscalePath {
  DEFAULT,
  CELLULAR,
  WAITING_FOR_CELLULAR,
}

data class RescueNetworkState(
    val wifi: RescueLinkState = RescueLinkState(),
    val cellular: RescueLinkState = RescueLinkState(),
    val tailscalePath: RescueTailscalePath = RescueTailscalePath.DEFAULT,
)

/**
 * 管理临时会话的两个底层 Network，并按目标地址绑定单个 socket。
 *
 * 这里不能使用 bindProcessToNetwork：Tailscale 控制面和局域网转发需要 同时走不同的 Network。没有匹配的 Wi-Fi 或蜂窝 Network 时返回
 * false， 由 Go 转发层关闭该连接，避免隐式回退到错误的接口。
 */
class RescueNetworkController(private val connectivityManager: ConnectivityManager) {
  private companion object {
    const val TAG = "RescueNetworkController"
  }

  private data class NetworkInfo(
      var capabilities: NetworkCapabilities,
      var linkProperties: LinkProperties,
  )

  private val lock = ReentrantLock()
  private val activeNetworks = mutableMapOf<Network, NetworkInfo>()
  private var callbackRegistered = false
  private var rescueMode = false
  private var rescueRoutes: List<IpPrefix> = emptyList()
  private val _networkState = MutableStateFlow(RescueNetworkState())
  val networkState: StateFlow<RescueNetworkState> = _networkState.asStateFlow()

  private val callback =
      object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
          lock.withLock {
            activeNetworks.putIfAbsent(
                network, NetworkInfo(NetworkCapabilities(), LinkProperties()))
            publishStateLocked()
          }
          TSLog.d(TAG, "网络可用: $network")
        }

        override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
          lock.withLock {
            activeNetworks[network]?.capabilities = capabilities
            publishStateLocked()
          }
        }

        override fun onLinkPropertiesChanged(network: Network, linkProperties: LinkProperties) {
          lock.withLock {
            activeNetworks[network]?.linkProperties = linkProperties
            publishStateLocked()
          }
        }

        override fun onLost(network: Network) {
          lock.withLock {
            activeNetworks.remove(network)
            publishStateLocked()
          }
          TSLog.d(TAG, "网络丢失: $network")
        }
      }

  /** 开始收集非 VPN 的 Wi-Fi 和蜂窝 Network；允许未通过互联网验证的 Wi-Fi。 */
  @SuppressLint("MissingPermission")
  fun start() {
    lock.withLock {
      if (callbackRegistered) return
      // 只排除 VPN，不要求 INTERNET/VALIDATED；无 WAN 的家庭 Wi-Fi 也必须可见。
      val request =
          NetworkRequest.Builder().addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build()
      connectivityManager.registerNetworkCallback(request, callback)
      // Network callbacks are asynchronous. Seed the snapshot so the first
      // PIN submission does not race the initial onAvailable/capability events.
      connectivityManager.allNetworks.forEach { network ->
        val capabilities = connectivityManager.getNetworkCapabilities(network) ?: return@forEach
        if (!capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) {
          return@forEach
        }
        val linkProperties = connectivityManager.getLinkProperties(network) ?: LinkProperties()
        activeNetworks[network] = NetworkInfo(capabilities, linkProperties)
      }
      callbackRegistered = true
      publishStateLocked()
    }
  }

  fun stop() {
    lock.withLock {
      if (!callbackRegistered) return
      connectivityManager.unregisterNetworkCallback(callback)
      callbackRegistered = false
      activeNetworks.clear()
      rescueMode = false
      rescueRoutes = emptyList()
      publishStateLocked()
    }
  }

  /** 设置救援目标路由；传 null 表示退出救援模式。 */
  fun setRescueRoute(cidr: String?) {
    setRescueRoutes(cidr?.let(::listOf))
    setRescueMode(cidr != null)
  }

  /**
   * 设置本次会话允许绑定到 Wi-Fi 的目标前缀。
   *
   * 默认仍然只传入当前网关 `/32`；这里支持列表是为了让服务端配置的 subnet router 和 Exit Node 默认路由可以被同一套 per-socket 选择逻辑处理。
   */
  fun setRescueRoutes(cidrs: List<String>?) {
    val parsed =
        cidrs?.map { cidr ->
          parseRoute(cidr) ?: throw IllegalArgumentException("临时会话路由必须是合法 IP CIDR: $cidr")
        } ?: emptyList()
    lock.withLock { rescueRoutes = parsed }
  }

  /** 让控制面/非 LAN 目标固定选择蜂窝，即使本次会话没有 Wi-Fi 目标路由。 */
  fun setRescueMode(enabled: Boolean) {
    lock.withLock {
      rescueMode = enabled
      publishStateLocked()
    }
  }

  fun isRescueMode(): Boolean = lock.withLock { rescueMode }

  fun cellularNetwork(): Network? = lock.withLock { pickCellularLocked() }

  fun wifiInterfaceName(): String? =
      lock.withLock { pickWifiLocked()?.let(activeNetworks::get)?.linkProperties?.interfaceName }

  fun cellularInterfaceName(): String? =
      lock.withLock {
        pickCellularLocked()?.let(activeNetworks::get)?.linkProperties?.interfaceName
      }

  /** 局域网内的配置服务器走 Wi-Fi；蜂窝模式下其他服务器固定走蜂窝。 */
  fun serverNetwork(host: String): Network? =
      lock.withLock {
        val target = parseNumericAddress(host)
        if (target != null) {
          val wifi = pickWifiLocked()
          val wifiInfo = wifi?.let(activeNetworks::get)
          if (wifi != null &&
              wifiInfo != null &&
              wifiInfo.linkProperties.routes.any { route ->
                !route.isDefaultRoute && route.matches(target)
              }) {
            TSLog.d(TAG, "配置服务器使用 Wi-Fi: host=$host network=$wifi")
            return@withLock wifi
          }
        }
        val network =
            if (rescueMode) pickCellularLocked() else NetworkChangeCallback.cachedDefaultNetwork
        network?.also { TSLog.d(TAG, "配置服务器使用当前 Tailscale 路径: host=$host network=$it") }
      }

  /** 返回当前 Wi-Fi 默认网关的精确 IPv4 /32；没有合适 Wi-Fi 时返回 null。 */
  fun currentWifiGatewayRoute(): String? {
    return lock.withLock {
      val info = pickWifiLocked()?.let { activeNetworks[it] } ?: return@withLock null
      val gateway =
          info.linkProperties.routes
              .filter { route -> route.isDefaultRoute && route.gateway is Inet4Address }
              .mapNotNull { route -> route.gateway?.hostAddress }
              .firstOrNull()
      gateway?.let { "$it/32" }
    }
  }

  /** 返回当前 Wi-Fi 接口的规范 IPv4 子网；例如 192.168.1.0/24。 */
  fun currentWifiSubnetRoute(): String? {
    return lock.withLock {
      val info = pickWifiLocked()?.let { activeNetworks[it] } ?: return@withLock null
      info.linkProperties.linkAddresses
          .asSequence()
          .filter { link -> link.address is Inet4Address && link.prefixLength in 1..31 }
          .map { link -> IpPrefix(link.address, link.prefixLength).toString() }
          .firstOrNull()
    }
  }

  /**
   * 在连接或监听前将 socket 绑定到目标所需的 Network。
   *
   * 目标命中救援前缀时只使用 Wi-Fi；其他 Tailscale 出站流量只使用蜂窝。 未配置救援路由时使用现有的 Tailscale 默认 Network，保持普通客户端行为。
   */
  fun bindSocket(fd: Int, destination: String): Boolean {
    val target = parseDestination(destination) ?: return false
    // The hook is installed for the whole Android backend lifecycle, but it
    // must be a no-op in ordinary Tailscale mode to preserve upstream routing.
    if (!isRescueMode()) return true
    val selectedNetwork =
        lock.withLock {
          if (rescueRoutes.any { route -> route.contains(target) }) {
            pickWifiLocked()
          } else {
            pickCellularLocked()
          }
        }
    val network = selectedNetwork ?: return false

    return try {
      ParcelFileDescriptor.fromFd(fd).use { pfd -> network.bindSocket(pfd.fileDescriptor) }
      true
    } catch (e: Exception) {
      TSLog.w(TAG, "按目标绑定 socket 失败: fd=$fd destination=$destination network=$network error=$e")
      false
    }
  }

  private fun pickCellularLocked(): Network? {
    return activeNetworks
        .asSequence()
        .filter { (_, info) ->
          info.capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) &&
              info.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN) &&
              info.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
        }
        .sortedByDescending { (_, info) ->
          info.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
        }
        .map { (network, _) -> network }
        .firstOrNull()
  }

  private fun pickWifiLocked(): Network? {
    return activeNetworks
        .asSequence()
        .filter { (_, info) ->
          info.capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) &&
              info.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN) &&
              info.linkProperties.routes.any { route ->
                route.isDefaultRoute && route.gateway != null
              }
        }
        .sortedByDescending { (_, info) ->
          info.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
        }
        .map { (network, _) -> network }
        .firstOrNull()
  }

  private fun publishStateLocked() {
    fun linkState(transport: Int): RescueLinkState {
      val info =
          activeNetworks.values
              .asSequence()
              .filter { candidate ->
                candidate.capabilities.hasTransport(transport) &&
                    candidate.capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
              }
              .sortedWith(
                  compareByDescending<NetworkInfo> { candidate ->
                        when (transport) {
                          NetworkCapabilities.TRANSPORT_WIFI ->
                              candidate.linkProperties.routes.any { route ->
                                route.isDefaultRoute && route.gateway != null
                              }
                          NetworkCapabilities.TRANSPORT_CELLULAR ->
                              candidate.capabilities.hasCapability(
                                  NetworkCapabilities.NET_CAPABILITY_INTERNET)
                          else -> true
                        }
                      }
                      .thenByDescending { candidate ->
                        candidate.capabilities.hasCapability(
                            NetworkCapabilities.NET_CAPABILITY_VALIDATED)
                      })
              .firstOrNull()
      return RescueLinkState(
          connected =
              when (transport) {
                NetworkCapabilities.TRANSPORT_WIFI ->
                    info?.linkProperties?.routes?.any { route ->
                      route.isDefaultRoute && route.gateway != null
                    } == true
                NetworkCapabilities.TRANSPORT_CELLULAR ->
                    info
                        ?.capabilities
                        ?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) == true
                else -> info != null
              },
          internetAvailable =
              info?.capabilities?.let { capabilities ->
                capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
                    capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
              } == true,
          interfaceName = info?.linkProperties?.interfaceName,
      )
    }

    _networkState.value =
        RescueNetworkState(
            wifi = linkState(NetworkCapabilities.TRANSPORT_WIFI),
            cellular = linkState(NetworkCapabilities.TRANSPORT_CELLULAR),
            tailscalePath =
                if (!rescueMode) {
                  RescueTailscalePath.DEFAULT
                } else if (pickCellularLocked() != null) {
                  RescueTailscalePath.CELLULAR
                } else {
                  RescueTailscalePath.WAITING_FOR_CELLULAR
                },
        )
  }

  private fun parseRoute(cidr: String): IpPrefix? {
    val slash = cidr.lastIndexOf('/')
    if (slash <= 0 || slash == cidr.lastIndex) return null
    val address =
        runCatching { InetAddress.getByName(cidr.substring(0, slash)) }.getOrNull() ?: return null
    val prefixLength = cidr.substring(slash + 1).toIntOrNull() ?: return null
    val maxPrefixLength = if (address is Inet4Address) 32 else 128
    if (prefixLength !in 0..maxPrefixLength) return null
    return IpPrefix(address, prefixLength)
  }

  private fun parseDestination(destination: String): InetAddress? {
    val host =
        if (destination.startsWith('[')) {
          destination.substringAfter('[').substringBefore(']')
        } else if (destination.count { it == ':' } == 1) {
          destination.substringBefore(':')
        } else if (runCatching { InetAddress.getByName(destination) }.isSuccess) {
          destination
        } else {
          destination.substringBeforeLast(':')
        }
    if (host.isBlank()) return null
    return runCatching { InetAddress.getByName(host) }.getOrNull()
  }

  private fun parseNumericAddress(host: String): InetAddress? {
    val normalized = host.removePrefix("[").removeSuffix("]")
    if (!normalized.contains(':') && !normalized.all { it.isDigit() || it == '.' }) return null
    return runCatching { InetAddress.getByName(normalized) }.getOrNull()
  }
}
