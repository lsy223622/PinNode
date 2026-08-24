// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn.ui.model

import android.net.Uri
import java.util.UUID
import kotlinx.serialization.Serializable
import kotlinx.serialization.Transient

class Ipn {

  // Represents the overall state of the Tailscale engine.
  enum class State(val value: Int) {
    NoState(0),
    InUseOtherUser(1),
    NeedsLogin(2),
    NeedsMachineAuth(3),
    Stopped(4),
    Starting(5),
    Running(6),
    // Stopping represents a state where a request to stop Tailscale has been issue but has not
    // completed. This state allows UI to optimistically reflect a stopped state, and to fallback if
    // necessary.
    Stopping(7);

    companion object {
      fun fromInt(value: Int): State {
        return State.values().firstOrNull { it.value == value } ?: NoState
      }
    }
  }

  // A notification message received on the Notify bus.  Fields will be populated based
  // on which NotifyWatchOpts were set when the Notifier was created.
  @Serializable
  data class Notify(
      val Version: String? = null,
      val ErrMessage: String? = null,
      val LoginFinished: Empty.Message? = null,
      val FilesWaiting: Empty.Message? = null,
      val OutgoingFiles: List<OutgoingFile>? = null,
      val State: Int? = null,
      var Prefs: Prefs? = null,
      var SelfChange: Tailcfg.Node? = null,
      var InitialStatus: IpnState.Status? = null,
      var PeersChanged: List<Tailcfg.Node>? = null,
      var PeersRemoved: List<NodeID>? = null,
      var UserProfiles: Map<String, Tailcfg.UserProfile>? = null,
      var Engine: EngineStatus? = null,
      var BrowseToURL: String? = null,
      var BackendLogId: String? = null,
      var LocalTCPPort: Int? = null,
      var IncomingFiles: List<PartialFile>? = null,
      var ClientVersion: Tailcfg.ClientVersion? = null,
      var TailFSShares: List<String>? = null,
      var Health: Health.State? = null,
  )

  @Serializable
  data class Prefs(
      var ControlURL: String = "",
      var RouteAll: Boolean = false,
      var ExitNodeIP: String? = null,
      var AutoExitNode: String? = null,
      var AllowsSingleHosts: Boolean = false,
      var CorpDNS: Boolean = false,
      var RunSSH: Boolean = false,
      var RunWebClient: Boolean = false,
      var WantRunning: Boolean = false,
      var LoggedOut: Boolean = false,
      var ShieldsUp: Boolean = false,
      var AdvertiseRoutes: List<String>? = null,
      var AdvertiseTags: List<String>? = null,
      var ExitNodeID: StableNodeID? = null,
      var ExitNodeAllowLANAccess: Boolean = false,
      var NoSNAT: Boolean = false,
      var NoStatefulFiltering: Boolean? = null,
      var NetfilterMode: Int? = null,
      var AppConnector: AppConnectorPrefs? = null,
      var PostureChecking: Boolean = false,
      var RemoteConfig: Boolean = false,
      var Config: Persist.Persist? = null,
      var ForceDaemon: Boolean = false,
      var HostName: String = "",
      var AutoUpdate: AutoUpdatePrefs? = AutoUpdatePrefs(true, true),
      var InternalExitNodePrior: String? = null,
  ) {

    // For the InternalExitNodePrior and ExitNodeId, these will treats the empty string as null to
    // simplify the downstream logic.

    val selectedExitNodeID: String?
      get() {
        return if (InternalExitNodePrior.isNullOrEmpty()) null else InternalExitNodePrior
      }

    val activeExitNodeID: String?
      get() {
        return if (ExitNodeID.isNullOrEmpty()) null else ExitNodeID
      }
  }

  @Serializable
  data class MaskedPrefs(
      var ControlURLSet: Boolean? = null,
      var RouteAllSet: Boolean? = null,
      var ExitNodeIPSet: Boolean? = null,
      var AutoExitNodeSet: Boolean? = null,
      var CorpDNSSet: Boolean? = null,
      var RunSSHSet: Boolean? = null,
      var RunWebClientSet: Boolean? = null,
      var ExitNodeIDSet: Boolean? = null,
      var ExitNodeAllowLANAccessSet: Boolean? = null,
      var WantRunningSet: Boolean? = null,
      var LoggedOutSet: Boolean? = null,
      var ShieldsUpSet: Boolean? = null,
      var AdvertiseRoutesSet: Boolean? = null,
      var NoSNATSet: Boolean? = null,
      var NoStatefulFilteringSet: Boolean? = null,
      var NetfilterModeSet: Boolean? = null,
      var AppConnectorSet: Boolean? = null,
      var PostureCheckingSet: Boolean? = null,
      var RemoteConfigSet: Boolean? = null,
      var AutoUpdateSet: AutoUpdatePrefsMask? = null,
      var ForceDaemonSet: Boolean? = null,
      var HostnameSet: Boolean? = null,
  ) {

    var ControlURL: String? = null
      set(value) {
        field = value
        ControlURLSet = true
      }

    var RouteAll: Boolean? = null
      set(value) {
        field = value
        RouteAllSet = true
      }

    var ExitNodeIP: String? = null
      set(value) {
        field = value
        ExitNodeIPSet = true
      }

    var AutoExitNode: String? = null
      set(value) {
        field = value
        AutoExitNodeSet = true
      }

    var CorpDNS: Boolean? = null
      set(value) {
        field = value
        CorpDNSSet = true
      }

    var RunSSH: Boolean? = null
      set(value) {
        field = value
        RunSSHSet = true
      }

    var RunWebClient: Boolean? = null
      set(value) {
        field = value
        RunWebClientSet = true
      }

    var ExitNodeID: StableNodeID? = null
      set(value) {
        field = value
        ExitNodeIDSet = true
      }

    var ExitNodeAllowLANAccess: Boolean? = null
      set(value) {
        field = value
        ExitNodeAllowLANAccessSet = true
      }

    var WantRunning: Boolean? = null
      set(value) {
        field = value
        WantRunningSet = true
      }

    var LoggedOut: Boolean? = null
      set(value) {
        field = value
        LoggedOutSet = true
      }

    var ShieldsUp: Boolean? = null
      set(value) {
        field = value
        ShieldsUpSet = true
      }

    var AdvertiseRoutes: List<String>? = null
      set(value) {
        field = value
        AdvertiseRoutesSet = true
      }

    var NoSNAT: Boolean? = null
      set(value) {
        field = value
        NoSNATSet = true
      }

    var NoStatefulFiltering: Boolean? = null
      set(value) {
        field = value
        NoStatefulFilteringSet = true
      }

    var NetfilterMode: Int? = null
      set(value) {
        field = value
        NetfilterModeSet = true
      }

    var AppConnector: AppConnectorPrefs? = null
      set(value) {
        field = value
        AppConnectorSet = true
      }

    var PostureChecking: Boolean? = null
      set(value) {
        field = value
        PostureCheckingSet = true
      }

    var RemoteConfig: Boolean? = null
      set(value) {
        field = value
        RemoteConfigSet = true
      }

    var AutoUpdate: AutoUpdatePrefs? = null

    var ForceDaemon: Boolean? = null
      set(value) {
        field = value
        ForceDaemonSet = true
      }

    var Hostname: String? = null
      set(value) {
        field = value
        HostnameSet = true
      }
  }

  @Serializable
  data class AutoUpdatePrefs(
      var Check: Boolean? = null,
      var Apply: Boolean? = null,
  )

  @Serializable
  data class AutoUpdatePrefsMask(
      var CheckSet: Boolean? = null,
      var ApplySet: Boolean? = null,
  )

  @Serializable
  data class AppConnectorPrefs(
      var Advertise: Boolean = false,
  )

  @Serializable
  data class EngineStatus(
      val RBytes: Long,
      val WBytes: Long,
      val NumLive: Int,
      val LivePeers: Map<String, IpnState.PeerStatusLite>,
  )

  @Serializable
  data class PartialFile(
      val Name: String,
      val Started: String,
      val DeclaredSize: Long,
      val Received: Long,
      val PartialPath: String? = null,
      var FinalPath: String? = null,
      val Done: Boolean? = null,
  )

  @Serializable
  data class OutgoingFile(
      val ID: String = "",
      val Name: String,
      val PeerID: StableNodeID = "",
      val Started: String = "",
      val DeclaredSize: Long,
      val Sent: Long = 0L,
      val PartialPath: String? = null,
      var FinalPath: String? = null,
      val Finished: Boolean = false,
      val Succeeded: Boolean = false,
  ) {
    @Transient lateinit var uri: Uri // only used on client

    fun prepare(peerId: StableNodeID): OutgoingFile {
      val f = copy(ID = UUID.randomUUID().toString(), PeerID = peerId)
      f.uri = uri
      return f
    }
  }

  @Serializable data class FileTarget(var Node: Tailcfg.Node, var PeerAPIURL: String)

  @Serializable
  data class Options(
      var FrontendLogID: String? = null,
      var UpdatePrefs: Prefs? = null,
      var AuthKey: String? = null,
  )
}

class Persist {
  @Serializable
  data class Persist(
      var PrivateMachineKey: String =
          "privkey:0000000000000000000000000000000000000000000000000000000000000000",
      var PrivateNodeKey: String =
          "privkey:0000000000000000000000000000000000000000000000000000000000000000",
      var OldPrivateNodeKey: String =
          "privkey:0000000000000000000000000000000000000000000000000000000000000000",
      var Provider: String = "",
  )
}

fun Ipn.MaskedPrefs.deepCopy(): Ipn.MaskedPrefs {
  return Ipn.MaskedPrefs().also {
    if (this.ControlURLSet == true) it.ControlURL = this.ControlURL
    if (this.RouteAllSet == true) it.RouteAll = this.RouteAll
    if (this.ExitNodeIPSet == true) it.ExitNodeIP = this.ExitNodeIP
    if (this.AutoExitNodeSet == true) it.AutoExitNode = this.AutoExitNode
    if (this.CorpDNSSet == true) it.CorpDNS = this.CorpDNS
    if (this.RunSSHSet == true) it.RunSSH = this.RunSSH
    if (this.RunWebClientSet == true) it.RunWebClient = this.RunWebClient
    if (this.ExitNodeIDSet == true) it.ExitNodeID = this.ExitNodeID
    if (this.ExitNodeAllowLANAccessSet == true)
        it.ExitNodeAllowLANAccess = this.ExitNodeAllowLANAccess
    if (this.WantRunningSet == true) it.WantRunning = this.WantRunning
    if (this.LoggedOutSet == true) it.LoggedOut = this.LoggedOut
    if (this.ShieldsUpSet == true) it.ShieldsUp = this.ShieldsUp
    if (this.AdvertiseRoutesSet == true) it.AdvertiseRoutes = this.AdvertiseRoutes
    if (this.NoSNATSet == true) it.NoSNAT = this.NoSNAT
    if (this.NoStatefulFilteringSet == true) it.NoStatefulFiltering = this.NoStatefulFiltering
    if (this.NetfilterModeSet == true) it.NetfilterMode = this.NetfilterMode
    if (this.AppConnectorSet == true) it.AppConnector = this.AppConnector
    if (this.PostureCheckingSet == true) it.PostureChecking = this.PostureChecking
    if (this.RemoteConfigSet == true) it.RemoteConfig = this.RemoteConfig
    if (this.AutoUpdateSet != null) it.AutoUpdateSet = this.AutoUpdateSet
    if (this.ForceDaemonSet == true) it.ForceDaemon = this.ForceDaemon
    if (this.HostnameSet == true) it.Hostname = this.Hostname
  }
}
