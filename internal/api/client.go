package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	Username   string
	Password   string
	Ticket     string
	CSRFToken  string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				// InsecureSkipVerify is intentional: Proxmox VE uses self-signed TLS
			// certificates by default. Do not remove without configuring a valid
			// certificate on the Proxmox host.
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func NewClientWithCredentials(baseURL, username, password string) (*Client, error) {
	c := NewClient(baseURL, "")
	c.Username = username
	c.Password = password
	if err := c.Login(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Login() error {
	data := url.Values{}
	data.Set("username", c.Username)
	data.Set("password", c.Password)
	resp, err := c.httpClient.PostForm(c.BaseURL+"/api2/json/access/ticket", data)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("login decode: %w", err)
	}
	if result.Data.Ticket == "" {
		return fmt.Errorf("login failed: empty ticket (wrong credentials?)")
	}
	c.Ticket = result.Data.Ticket
	c.CSRFToken = result.Data.CSRFPreventionToken
	return nil
}

// maxResponseBytes: limite alla risposta letta da Proxmox (una lista cluster molto grande resta ben sotto).
const maxResponseBytes = 32 << 20

func (c *Client) doRequest(method, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequest(method, c.BaseURL+"/api2/json"+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.Token)
	} else if c.Ticket != "" {
		req.Header.Set("Cookie", "PVEAuthCookie="+c.Ticket)
		if method != "GET" {
			req.Header.Set("CSRFPreventionToken", c.CSRFToken)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	// Tetto alla risposta: un guest o un server compromesso non deve poter riempire la memoria del proxy.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxResponseBytes {
		return nil, fmt.Errorf("risposta di Proxmox troppo grande (oltre %d MiB) su %s", maxResponseBytes>>20, path)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("proxmox %d on %s: %s", resp.StatusCode, path, string(b))
	}
	return b, nil
}

func (c *Client) Get(path string) (json.RawMessage, error) {
	b, err := c.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var r struct{ Data json.RawMessage `json:"data"` }
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

func (c *Client) Post(path string, params url.Values) (json.RawMessage, error) {
	b, err := c.doRequest("POST", path, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	var r struct{ Data json.RawMessage `json:"data"` }
	json.Unmarshal(b, &r)
	return r.Data, nil
}

func (c *Client) Put(path string, params url.Values) (json.RawMessage, error) {
	b, err := c.doRequest("PUT", path, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	var r struct{ Data json.RawMessage `json:"data"` }
	json.Unmarshal(b, &r)
	return r.Data, nil
}

func (c *Client) Delete(path string) (json.RawMessage, error) {
	b, err := c.doRequest("DELETE", path, nil)
	if err != nil {
		return nil, err
	}
	var r struct{ Data json.RawMessage `json:"data"` }
	json.Unmarshal(b, &r)
	return r.Data, nil
}

// ── Cluster ──────────────────────────────────────────────────────────────────

func (c *Client) GetClusterStatus() (json.RawMessage, error) { return c.Get("/cluster/status") }
func (c *Client) GetClusterResources(resType string) (json.RawMessage, error) {
	path := "/cluster/resources"
	if resType != "" {
		path += "?type=" + resType
	}
	return c.Get(path)
}
func (c *Client) GetClusterTasks() (json.RawMessage, error)  { return c.Get("/cluster/tasks") }
func (c *Client) GetNextVMID() (json.RawMessage, error)      { return c.Get("/cluster/nextid") }

// ── Nodes ─────────────────────────────────────────────────────────────────────

func (c *Client) GetNodes() (json.RawMessage, error) { return c.Get("/nodes") }
func (c *Client) GetNodeStatus(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/status")
}
func (c *Client) GetNodeRRD(node, tf string) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/rrddata?timeframe=%s", node, tf))
}
func (c *Client) GetNodeStorage(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/storage")
}
func (c *Client) GetNodeNetworks(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/network")
}
func (c *Client) NodeCommand(node, cmd string) (json.RawMessage, error) {
	return c.Post("/nodes/"+node+"/status", url.Values{"command": {cmd}})
}

// ── QEMU VMs ─────────────────────────────────────────────────────────────────

func (c *Client) GetVMs(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/qemu")
}
func (c *Client) GetVMStatus(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/status/current", node, vmid))
}
func (c *Client) GetVMConfig(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid))
}
func (c *Client) GetVMFSInfo(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/agent/get-fsinfo", node, vmid))
}
func (c *Client) GetVMRRD(node string, vmid int, tf string) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/rrddata?timeframe=%s", node, vmid, tf))
}
func (c *Client) VMAction(node string, vmid int, action string, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	return c.Post(fmt.Sprintf("/nodes/%s/qemu/%d/status/%s", node, vmid, action), params)
}
func (c *Client) VMMigrate(node string, vmid int, target string, online bool) (json.RawMessage, error) {
	v := url.Values{"target": {target}}
	if online {
		v.Set("online", "1")
	}
	return c.Post(fmt.Sprintf("/nodes/%s/qemu/%d/migrate", node, vmid), v)
}
// GetMigratePrecheck is Proxmox's own check before a QEMU migration (read only): allowed_nodes, local_disks,
// local_resources, mapped-resources.
func (c *Client) GetMigratePrecheck(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/migrate", node, vmid))
}

// GetHAResources lists the guests managed by HA (read only).
func (c *Client) GetHAResources() (json.RawMessage, error) { return c.Get("/cluster/ha/resources") }

// LXCMigrate migrates a container; Proxmox moves a running container only in restart mode (short downtime).
func (c *Client) LXCMigrate(node string, vmid int, target string, restart bool) (json.RawMessage, error) {
	v := url.Values{"target": {target}}
	if restart {
		v.Set("restart", "1")
	}
	return c.Post(fmt.Sprintf("/nodes/%s/lxc/%d/migrate", node, vmid), v)
}

// GetTaskStatus reads a task (read only): status running|stopped and exitstatus when stopped.
// GetStorageContent lists the volumes of a storage on a node for one content type ("snippets", "iso", "images"...).
// The caller validates node, storage and content before calling.
func (c *Client) GetStorageContent(node, storage, content string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/storage/" + storage + "/content?content=" + url.QueryEscape(content))
}

func (c *Client) GetTaskStatus(node, upid string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/tasks/" + url.PathEscape(upid) + "/status")
}

func (c *Client) VMSnapshot(node string, vmid int, name, desc string) (json.RawMessage, error) {
	v := url.Values{"snapname": {name}}
	if desc != "" {
		v.Set("description", desc)
	}
	return c.Post(fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", node, vmid), v)
}
func (c *Client) GetVMSnapshots(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", node, vmid))
}

// VMClone performs a full clone of a VM/template
func (c *Client) VMClone(node string, vmid, newid int, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("newid", fmt.Sprintf("%d", newid))
	if params.Get("full") == "" {
		params.Set("full", "1")
	}
	return c.Post(fmt.Sprintf("/nodes/%s/qemu/%d/clone", node, vmid), params)
}

// VMSetConfig updates VM configuration (cpu, memory, etc.)
func (c *Client) VMSetConfig(node string, vmid int, params url.Values) (json.RawMessage, error) {
	return c.Put(fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), params)
}

// GetGuestConfig / SetGuestConfig read and write the config of a QEMU VM or LXC container. kind must be "qemu" or
// "lxc" (validated by the caller; anything else is refused here too).
func (c *Client) GetGuestConfig(node, kind string, vmid int) (json.RawMessage, error) {
	if kind != "qemu" && kind != "lxc" {
		return nil, fmt.Errorf("tipo non valido")
	}
	return c.Get(fmt.Sprintf("/nodes/%s/%s/%d/config", node, kind, vmid))
}
func (c *Client) SetGuestConfig(node, kind string, vmid int, params url.Values) (json.RawMessage, error) {
	if kind != "qemu" && kind != "lxc" {
		return nil, fmt.Errorf("tipo non valido")
	}
	return c.Put(fmt.Sprintf("/nodes/%s/%s/%d/config", node, kind, vmid), params)
}

// VMResizeDisk resizes a VM disk. size is the NEW total size e.g. "+10G" or "50G"
func (c *Client) VMResizeDisk(node string, vmid int, disk, size string) (json.RawMessage, error) {
	return c.Put(fmt.Sprintf("/nodes/%s/qemu/%d/resize", node, vmid),
		url.Values{"disk": {disk}, "size": {size}})
}

// VMSetCloudInit sets cloud-init parameters
func (c *Client) VMSetCloudInit(node string, vmid int, params url.Values) (json.RawMessage, error) {
	return c.Put(fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), params)
}

func (c *Client) DeleteVM(node string, vmid int) (json.RawMessage, error) {
	return c.Delete(fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid))
}

func (c *Client) TermproxyCreate(node string, vmid int, proxmoxBaseURL string) (json.RawMessage, error) {
	referer := fmt.Sprintf("%s/?console=kvm&xtermjs=1&vmid=%d&node=%s&cmd=", proxmoxBaseURL, vmid, node)
	path := fmt.Sprintf("/nodes/%s/qemu/%d/termproxy", node, vmid)
	req, err := http.NewRequest("POST", c.BaseURL+"/api2/json"+path, strings.NewReader(url.Values{}.Encode()))
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.Token)
	} else if c.Ticket != "" {
		req.Header.Set("Cookie", "PVEAuthCookie="+c.Ticket)
		req.Header.Set("CSRFPreventionToken", c.CSRFToken)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", referer)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("proxmox %d: %s", resp.StatusCode, string(b))
	}
	var r struct{ Data json.RawMessage `json:"data"` }
	json.Unmarshal(b, &r)
	return r.Data, nil
}

// GetAccessTicket autentica con username+password e ritorna (PVEAuthCookie, CSRFToken, error).
// Necessario per autenticare il WebSocket vncwebsocket, che non accetta API token.
func GetAccessTicket(baseURL, username, password string) (string, string, error) {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
	}
	c := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)
	resp, err := c.PostForm(baseURL+"/api2/json/access/ticket", data)
	if err != nil {
		return "", "", fmt.Errorf("access/ticket: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Ticket              string `json:"ticket"`
			CSRFPreventionToken string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}
	if result.Data.Ticket == "" {
		return "", "", fmt.Errorf("autenticazione fallita (credenziali errate?)")
	}
	return result.Data.Ticket, result.Data.CSRFPreventionToken, nil
}

func (c *Client) AgentExec(node string, vmid int, args []string) (json.RawMessage, error) {
	v := url.Values{}
	for _, a := range args {
		v.Add("command", a)
	}
	return c.Post(fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec", node, vmid), v)
}

func (c *Client) GetVMOSInfo(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/agent/get-osinfo", node, vmid))
}

// ── Report data (read only) ─────────────────────────────────────────────────

var (
	rrdTimeframes = map[string]bool{"hour": true, "day": true, "week": true, "month": true, "year": true}
	rrdCFs        = map[string]bool{"AVERAGE": true, "MAX": true}
)

func rrdQuery(tf, cf string) (string, error) {
	if !rrdTimeframes[tf] || !rrdCFs[cf] {
		return "", fmt.Errorf("timeframe o funzione di consolidamento non validi")
	}
	return "?timeframe=" + tf + "&cf=" + cf, nil
}

// GetNodeRRDcf, GetGuestRRDcf and GetStorageRRD read Proxmox RRD with an explicit consolidation function.
func (c *Client) GetNodeRRDcf(node, tf, cf string) (json.RawMessage, error) {
	q, err := rrdQuery(tf, cf)
	if err != nil {
		return nil, err
	}
	return c.Get("/nodes/" + node + "/rrddata" + q)
}

// GetGuestRRDcf kind is "qemu" or "lxc".
func (c *Client) GetGuestRRDcf(node, kind string, vmid int, tf, cf string) (json.RawMessage, error) {
	q, err := rrdQuery(tf, cf)
	if err != nil || (kind != "qemu" && kind != "lxc") {
		return nil, fmt.Errorf("parametri RRD non validi")
	}
	return c.Get(fmt.Sprintf("/nodes/%s/%s/%d/rrddata%s", node, kind, vmid, q))
}

func (c *Client) GetStorageRRD(node, storage, tf, cf string) (json.RawMessage, error) {
	q, err := rrdQuery(tf, cf)
	if err != nil {
		return nil, err
	}
	return c.Get("/nodes/" + node + "/storage/" + url.PathEscape(storage) + "/rrddata" + q)
}

// GetNodeTasks lists a node's task history (Proxmox keeps about two years); limit is capped by the caller.
func (c *Client) GetNodeTasks(node string, limit int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/tasks?limit=%d", node, limit))
}

// ── PCI passthrough (read only) ─────────────────────────────────────────────

func (c *Client) GetNodePCI(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/hardware/pci")
}
func (c *Client) GetPCIMappings() (json.RawMessage, error) { return c.Get("/cluster/mapping/pci") }

// ── Ceph (read only) ────────────────────────────────────────────────────────

func (c *Client) GetCephStatus() (json.RawMessage, error) { return c.Get("/cluster/ceph/status") }
func (c *Client) GetCephFlags() (json.RawMessage, error)  { return c.Get("/cluster/ceph/flags") }
func (c *Client) GetCephOSD(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/ceph/osd")
}
// GetCephCrush returns the decompiled CRUSH map (a JSON string with the text).
func (c *Client) GetCephCrush(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/ceph/crush")
}
func (c *Client) GetCephPools(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/ceph/pool")
}

// GetCephServices lists mon, mgr or mds daemons (kind is validated by the caller's constant arguments).
func (c *Client) GetCephServices(node, kind string) (json.RawMessage, error) {
	switch kind {
	case "mon", "mgr", "mds":
		return c.Get("/nodes/" + node + "/ceph/" + kind)
	}
	return nil, fmt.Errorf("tipo di servizio Ceph non valido")
}

func (c *Client) GetVMNetworkInterfaces(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid))
}

func (c *Client) AgentExecStatus(node string, vmid, pid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec-status?pid=%d", node, vmid, pid))
}

// ── LXC ──────────────────────────────────────────────────────────────────────

func (c *Client) GetContainers(node string) (json.RawMessage, error) {
	return c.Get("/nodes/" + node + "/lxc")
}
func (c *Client) GetContainerConfig(node string, vmid int) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/lxc/%d/config", node, vmid))
}
func (c *Client) ContainerAction(node string, vmid int, action string, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	return c.Post(fmt.Sprintf("/nodes/%s/lxc/%d/status/%s", node, vmid, action), params)
}
func (c *Client) ContainerSnapshot(node string, vmid int, name string) (json.RawMessage, error) {
	return c.Post(fmt.Sprintf("/nodes/%s/lxc/%d/snapshot", node, vmid), url.Values{"snapname": {name}})
}
func (c *Client) GetContainerRRD(node string, vmid int, tf string) (json.RawMessage, error) {
	return c.Get(fmt.Sprintf("/nodes/%s/lxc/%d/rrddata?timeframe=%s", node, vmid, tf))
}
func (c *Client) DeleteContainer(node string, vmid int) (json.RawMessage, error) {
	return c.Delete(fmt.Sprintf("/nodes/%s/lxc/%d", node, vmid))
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

// WaitForTask polls a Proxmox UPID task until it stops or timeout is exceeded.
// UPID format: UPID:node:pid:pstart:starttime:type:id:user:
func (c *Client) WaitForTask(upidJSON json.RawMessage, timeout time.Duration) error {
	var upid string
	if err := json.Unmarshal(upidJSON, &upid); err != nil {
		upid = strings.Trim(string(upidJSON), `"`)
	}
	if upid == "" {
		return fmt.Errorf("UPID vuoto")
	}
	parts := strings.Split(upid, ":")
	if len(parts) < 2 {
		return fmt.Errorf("UPID non valido: %s", upid)
	}
	node := parts[1]

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := c.Get("/nodes/" + node + "/tasks/" + url.PathEscape(upid) + "/status")
		if err == nil {
			var s struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			}
			if json.Unmarshal(status, &s) == nil && s.Status == "stopped" {
				if s.ExitStatus != "OK" {
					return fmt.Errorf("task fallito: %s", s.ExitStatus)
				}
				return nil
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timeout %v attesa task", timeout)
}

// ── Storage ───────────────────────────────────────────────────────────────────

func (c *Client) GetStorage() (json.RawMessage, error) { return c.Get("/storage") }
func (c *Client) GetPools() (json.RawMessage, error)   { return c.Get("/pools") }
