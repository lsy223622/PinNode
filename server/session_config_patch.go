package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// SessionConfigPatch keeps field presence separate from a field's zero value.
// That is required for explicit false, empty strings, and empty route lists.
type SessionConfigPatch struct {
	NetworkMode            *string          `json:"networkMode"`
	VPNEnabled             *bool            `json:"vpnEnabled"`
	AcceptRoutes           *bool            `json:"acceptRoutes"`
	AcceptDNS              *bool            `json:"acceptDNS"`
	TailscaleIP            *string          `json:"tailscaleIp"`
	UseExitNode            *bool            `json:"useExitNode"`
	ExitNodeID             *string          `json:"exitNodeId"`
	ExitNodeIP             *string          `json:"exitNodeIp"`
	AutoExitNode           *string          `json:"autoExitNode"`
	ExitNodeAllowLANAccess *bool            `json:"exitNodeAllowLanAccess"`
	SubnetRouter           *bool            `json:"subnetRouter"`
	AutoGatewayRoute       *bool            `json:"autoGatewayRoute"`
	AutoWiFiSubnetRoute    *bool            `json:"autoWiFiSubnetRoute"`
	AdvertiseRoutes        *[]string        `json:"advertiseRoutes"`
	AdvertiseExitNode      *bool            `json:"advertiseExitNode"`
	DisableSNAT            *bool            `json:"disableSNAT"`
	NoStatefulFiltering    *bool            `json:"noStatefulFiltering"`
	ShieldsUp              *bool            `json:"shieldsUp"`
	RunSSHServer           *bool            `json:"runSSHServer"`
	RunWebClient           *bool            `json:"runWebClient"`
	PostureChecking        *bool            `json:"postureChecking"`
	RemoteConfig           *bool            `json:"remoteConfig"`
	Hostname               *string          `json:"hostname"`
	NetfilterMode          *string          `json:"netfilterMode"`
	AppConnector           *bool            `json:"appConnector"`
	ExitPolicy             *ExitPolicyPatch `json:"exitPolicy"`
}

type ExitPolicyPatch struct {
	OnAppClose         *bool   `json:"onAppClose"`
	NetworkChange      *string `json:"networkChange"`
	AfterConfigSeconds *int64  `json:"afterConfigSeconds"`
	AfterLoginSeconds  *int64  `json:"afterLoginSeconds"`
	At                 *string `json:"at"`
}

type adminSessionConfigUpdateRequest struct {
	ExpectedRevision int64
	Config           SessionConfigPatch
	ConfirmHighRisk  bool
}

func (p *SessionConfigPatch) UnmarshalJSON(data []byte) error {
	fields, err := strictJSONObject(data)
	if err != nil {
		return err
	}
	parsed, err := decodeSessionConfigPatch(fields)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

func (r *adminSessionConfigUpdateRequest) UnmarshalJSON(data []byte) error {
	fields, err := strictJSONObject(data)
	if err != nil {
		return err
	}
	for key := range fields {
		switch key {
		case "expectedRevision", "config", "confirmHighRisk":
		default:
			return fmt.Errorf("unknown field %q", key)
		}
	}
	expectedRaw, ok := fields["expectedRevision"]
	if !ok || isJSONNull(expectedRaw) {
		return fmt.Errorf("expectedRevision is required")
	}
	if err := json.Unmarshal(expectedRaw, &r.ExpectedRevision); err != nil {
		return fmt.Errorf("expectedRevision is invalid")
	}
	configRaw, ok := fields["config"]
	if !ok || isJSONNull(configRaw) {
		return fmt.Errorf("config is required")
	}
	if err := json.Unmarshal(configRaw, &r.Config); err != nil {
		return fmt.Errorf("config patch is invalid")
	}
	if raw, ok := fields["confirmHighRisk"]; ok {
		if isJSONNull(raw) || json.Unmarshal(raw, &r.ConfirmHighRisk) != nil {
			return fmt.Errorf("confirmHighRisk is invalid")
		}
	}
	return nil
}

func strictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("value must be an object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("object key is invalid")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[key] = raw
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim('}') {
		return nil, fmt.Errorf("object is not closed")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON must contain one object")
	}
	return fields, nil
}

func decodeSessionConfigPatch(fields map[string]json.RawMessage) (SessionConfigPatch, error) {
	var patch SessionConfigPatch
	for key, raw := range fields {
		switch key {
		case "networkMode":
			if err := decodePatchString(raw, &patch.NetworkMode); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "vpnEnabled":
			if err := decodePatchBool(raw, &patch.VPNEnabled); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "acceptRoutes":
			if err := decodePatchBool(raw, &patch.AcceptRoutes); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "acceptDNS":
			if err := decodePatchBool(raw, &patch.AcceptDNS); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "tailscaleIp":
			if err := decodePatchString(raw, &patch.TailscaleIP); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "useExitNode":
			if err := decodePatchBool(raw, &patch.UseExitNode); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "exitNodeId":
			if err := decodePatchString(raw, &patch.ExitNodeID); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "exitNodeIp":
			if err := decodePatchString(raw, &patch.ExitNodeIP); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "autoExitNode":
			if err := decodePatchString(raw, &patch.AutoExitNode); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "exitNodeAllowLanAccess":
			if err := decodePatchBool(raw, &patch.ExitNodeAllowLANAccess); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "subnetRouter":
			if err := decodePatchBool(raw, &patch.SubnetRouter); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "autoGatewayRoute":
			if err := decodePatchBool(raw, &patch.AutoGatewayRoute); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "autoWiFiSubnetRoute":
			if err := decodePatchBool(raw, &patch.AutoWiFiSubnetRoute); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "advertiseRoutes":
			if err := decodePatchRoutes(raw, &patch.AdvertiseRoutes); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "advertiseExitNode":
			if err := decodePatchBool(raw, &patch.AdvertiseExitNode); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "disableSNAT":
			if err := decodePatchBool(raw, &patch.DisableSNAT); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "noStatefulFiltering":
			if err := decodePatchBool(raw, &patch.NoStatefulFiltering); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "shieldsUp":
			if err := decodePatchBool(raw, &patch.ShieldsUp); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "runSSHServer":
			if err := decodePatchBool(raw, &patch.RunSSHServer); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "runWebClient":
			if err := decodePatchBool(raw, &patch.RunWebClient); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "postureChecking":
			if err := decodePatchBool(raw, &patch.PostureChecking); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "remoteConfig":
			if err := decodePatchBool(raw, &patch.RemoteConfig); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "hostname":
			if err := decodePatchString(raw, &patch.Hostname); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "netfilterMode":
			if err := decodePatchString(raw, &patch.NetfilterMode); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "appConnector":
			if err := decodePatchBool(raw, &patch.AppConnector); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		case "exitPolicy":
			if err := decodeExitPolicyPatch(raw, &patch.ExitPolicy); err != nil {
				return SessionConfigPatch{}, fmt.Errorf("%s is invalid", key)
			}
		default:
			return SessionConfigPatch{}, fmt.Errorf("unknown field %q", key)
		}
	}
	return patch, nil
}

func decodeExitPolicyPatch(raw json.RawMessage, target **ExitPolicyPatch) error {
	if isJSONNull(raw) {
		return fmt.Errorf("null is not allowed")
	}
	fields, err := strictJSONObject(raw)
	if err != nil {
		return err
	}
	patch := &ExitPolicyPatch{}
	for key, value := range fields {
		switch key {
		case "onAppClose":
			if err := decodePatchBool(value, &patch.OnAppClose); err != nil {
				return err
			}
		case "networkChange":
			if err := decodePatchString(value, &patch.NetworkChange); err != nil {
				return err
			}
		case "afterConfigSeconds":
			if err := decodePatchInt64(value, &patch.AfterConfigSeconds); err != nil {
				return err
			}
		case "afterLoginSeconds":
			if err := decodePatchInt64(value, &patch.AfterLoginSeconds); err != nil {
				return err
			}
		case "at":
			if err := decodePatchString(value, &patch.At); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown field %q", key)
		}
	}
	*target = patch
	return nil
}

func decodePatchString(raw json.RawMessage, target **string) error {
	if isJSONNull(raw) {
		return fmt.Errorf("null is not allowed")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*target = &value
	return nil
}

func decodePatchBool(raw json.RawMessage, target **bool) error {
	if isJSONNull(raw) {
		return fmt.Errorf("null is not allowed")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*target = &value
	return nil
}

func decodePatchInt64(raw json.RawMessage, target **int64) error {
	if isJSONNull(raw) {
		return fmt.Errorf("null is not allowed")
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*target = &value
	return nil
}

func decodePatchRoutes(raw json.RawMessage, target **[]string) error {
	if isJSONNull(raw) {
		return fmt.Errorf("null is not allowed")
	}
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return fmt.Errorf("routes must be an array")
	}
	*target = &value
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func applySessionConfigPatch(current SessionConfig, patch SessionConfigPatch) SessionConfig {
	if patch.NetworkMode != nil {
		current.NetworkMode = *patch.NetworkMode
	}
	if patch.VPNEnabled != nil {
		current.VPNEnabled = *patch.VPNEnabled
	}
	if patch.AcceptRoutes != nil {
		current.AcceptRoutes = *patch.AcceptRoutes
	}
	if patch.AcceptDNS != nil {
		current.AcceptDNS = *patch.AcceptDNS
	}
	if patch.TailscaleIP != nil {
		current.TailscaleIP = *patch.TailscaleIP
	}
	if patch.UseExitNode != nil {
		current.UseExitNode = *patch.UseExitNode
	}
	if patch.ExitNodeID != nil {
		current.ExitNodeID = *patch.ExitNodeID
	}
	if patch.ExitNodeIP != nil {
		current.ExitNodeIP = *patch.ExitNodeIP
	}
	if patch.AutoExitNode != nil {
		current.AutoExitNode = *patch.AutoExitNode
	}
	if patch.ExitNodeAllowLANAccess != nil {
		current.ExitNodeAllowLANAccess = *patch.ExitNodeAllowLANAccess
	}
	if patch.SubnetRouter != nil {
		current.SubnetRouter = *patch.SubnetRouter
	}
	if patch.AutoGatewayRoute != nil {
		current.AutoGatewayRoute = *patch.AutoGatewayRoute
	}
	if patch.AutoWiFiSubnetRoute != nil {
		current.AutoWiFiSubnetRoute = *patch.AutoWiFiSubnetRoute
	}
	if patch.AdvertiseRoutes != nil {
		current.AdvertiseRoutes = append([]string{}, (*patch.AdvertiseRoutes)...)
	}
	if patch.AdvertiseExitNode != nil {
		current.AdvertiseExitNode = *patch.AdvertiseExitNode
	}
	if patch.DisableSNAT != nil {
		current.DisableSNAT = *patch.DisableSNAT
	}
	if patch.NoStatefulFiltering != nil {
		current.NoStatefulFiltering = *patch.NoStatefulFiltering
	}
	if patch.ShieldsUp != nil {
		current.ShieldsUp = *patch.ShieldsUp
	}
	if patch.RunSSHServer != nil {
		current.RunSSHServer = *patch.RunSSHServer
	}
	if patch.RunWebClient != nil {
		current.RunWebClient = *patch.RunWebClient
	}
	if patch.PostureChecking != nil {
		current.PostureChecking = *patch.PostureChecking
	}
	if patch.RemoteConfig != nil {
		current.RemoteConfig = *patch.RemoteConfig
	}
	if patch.Hostname != nil {
		current.Hostname = *patch.Hostname
	}
	if patch.NetfilterMode != nil {
		current.NetfilterMode = *patch.NetfilterMode
	}
	if patch.AppConnector != nil {
		current.AppConnector = *patch.AppConnector
	}
	if patch.ExitPolicy != nil {
		if patch.ExitPolicy.OnAppClose != nil {
			current.ExitPolicy.OnAppClose = *patch.ExitPolicy.OnAppClose
		}
		if patch.ExitPolicy.NetworkChange != nil {
			current.ExitPolicy.NetworkChange = *patch.ExitPolicy.NetworkChange
		}
		if patch.ExitPolicy.AfterConfigSeconds != nil {
			current.ExitPolicy.AfterConfigSeconds = *patch.ExitPolicy.AfterConfigSeconds
		}
		if patch.ExitPolicy.AfterLoginSeconds != nil {
			current.ExitPolicy.AfterLoginSeconds = *patch.ExitPolicy.AfterLoginSeconds
		}
		if patch.ExitPolicy.At != nil {
			current.ExitPolicy.At = *patch.ExitPolicy.At
		}
	}
	return current
}
