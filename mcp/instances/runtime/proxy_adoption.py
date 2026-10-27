"""Bounded Caddy adapter. Imported by local tests; stdin execution is the SSH entrypoint.

No service restart, TLS-store changes, host firewall changes or application header
policy. Only an explicitly selected source file can be atomically replaced.
"""
import base64
import copy
import fcntl
import hashlib
import http.client
import json
import os
import pathlib
import re
import selectors
import socket
import ssl
import stat
import subprocess
import sys
import time

LIMIT = 2 * 1024 * 1024
DELAY = 24 * 60 * 60 * 1000000000


class Refused(Exception):
    pass


def digest(value):
    if not isinstance(value, bytes):
        value = json.dumps(value, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(value).hexdigest()


def safe_path(name, missing=False):
    p = pathlib.Path(name)
    if not p.is_absolute() or str(p) != name or ".." in p.parts:
        raise Refused("unsafe_configuration_path")
    for part in [*reversed(p.parents), p]:
        try:
            s = os.lstat(part)
        except FileNotFoundError:
            if missing:
                continue
            raise Refused("missing_configuration_path")
        if stat.S_ISLNK(s.st_mode):
            raise Refused("symlink_configuration_path")
        if part == p and (not stat.S_ISREG(s.st_mode) or s.st_nlink != 1):
            raise Refused("configuration_must_be_a_regular_single_link_file")
        if part != p and not stat.S_ISDIR(s.st_mode):
            raise Refused("unsafe_configuration_parent")
    return p


def read_file(name):
    p = safe_path(name)
    s = p.stat()
    if s.st_size > LIMIT or s.st_uid not in (0, os.geteuid()) or s.st_mode & 0o022:
        raise Refused("configuration_size_or_permissions_unsafe")
    with open(p, "rb") as stream:
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise Refused("configuration_too_large")
    return raw


def atomic_file(name, raw, metadata=None):
    safe_path(name, missing=True)
    if len(raw) > LIMIT:
        raise Refused("configuration_too_large")
    temp = name + ".apteva-tmp-" + os.urandom(8).hex()
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(raw)
            stream.flush()
            if metadata:
                os.fchmod(stream.fileno(), metadata[0])
                os.fchown(stream.fileno(), metadata[1], metadata[2])
            os.fsync(stream.fileno())
        os.replace(temp, name)
        parent = os.open(str(pathlib.Path(name).parent), os.O_RDONLY)
        try:
            os.fsync(parent)
        finally:
            os.close(parent)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def command(args, data=None, timeout=6, cwd=None):
    # Bound output while reading, rather than collecting unlimited subprocess output.
    p = subprocess.Popen(args, stdin=subprocess.PIPE if data is not None else subprocess.DEVNULL,
                         stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=cwd)
    selector = selectors.DefaultSelector()
    try:
        if data is not None:
            p.stdin.write(data)
            p.stdin.close()
        selector.register(p.stdout, selectors.EVENT_READ)
        chunks, length, end = [], 0, time.monotonic() + timeout
        while selector.get_map():
            if time.monotonic() > end:
                raise Refused("proxy_command_timeout")
            for key, _ in selector.select(min(.1, max(0, end - time.monotonic()))):
                raw = os.read(key.fileobj.fileno(), 65536)
                if not raw:
                    selector.unregister(key.fileobj)
                    continue
                length += len(raw)
                if length > LIMIT:
                    raise Refused("proxy_command_output_too_large")
                chunks.append(raw)
        if p.wait(timeout=max(.01, end - time.monotonic())) != 0:
            raise Refused("proxy_command_failed")
        return b"".join(chunks)
    finally:
        selector.close()
        if p.poll() is None:
            p.kill()
        p.wait()
        p.stdout.close()


class UnixHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


class Proxy:
    def __init__(self, payload):
        self.p = payload
        self.r = payload["request"]
        self.spec = self.r["proxy"]
        self.host = self.spec["hostname"]
        self.source = self.spec["site_file"]
        self.admin = self.spec["admin"]
        self.identity = {k: payload[k] for k in ("install_id", "host_key")}
        self.identity.update(hostname=self.host, proxy=self.spec, root=payload["root"])
        self.owner_file = self.source + ".apteva-owner.json"
        self.journal = self.source + ".apteva-journal.json"
        self.cwd = None

    def service(self):
        raw = command(["systemctl", "show", self.spec["service"], "--no-pager",
                       "--property=MainPID,ActiveState,FragmentPath,ExecStart,WorkingDirectory,DropInPaths"])
        props = dict(row.split("=", 1) for row in raw.decode().splitlines() if "=" in row)
        if props.get("ActiveState") != "active" or not props.get("MainPID", "").isdigit():
            raise Refused("proxy_service_not_active")
        pid = props["MainPID"]
        binary = safe_path(self.spec["binary"])
        if os.path.realpath("/proc/" + pid + "/exe") != str(binary):
            raise Refused("proxy_service_binary_mismatch")
        args = pathlib.Path("/proc/" + pid + "/cmdline").read_bytes().split(b"\0")
        args = [v.decode() for v in args if v]
        if "run" not in args or "--resume" in args:
            raise Refused("unsupported_proxy_service_command")
        def option(key):
            if key in args:
                at = args.index(key) + 1
                return args[at] if at < len(args) else None
            return next((a.split("=", 1)[1] for a in args if a.startswith(key + "=")), None)
        if option("--config") != self.spec["config"]:
            raise Refused("proxy_service_configuration_mismatch")
        adapter = option("--adapter")
        if self.spec["format"] == "caddyfile" and adapter not in (None, "caddyfile"):
            raise Refused("proxy_service_adapter_mismatch")
        if self.spec["format"] == "json" and adapter is not None:
            raise Refused("proxy_service_adapter_mismatch")
        self.cwd = props.get("WorkingDirectory") or "/"
        self.proxy_pid = pid
        files = [props.get("FragmentPath", "")] + props.get("DropInPaths", "").split()
        definitions = {name: digest(read_file(os.path.realpath(name))) for name in files if name}
        # PIDs and systemd's ExecStart result fields change on a normal restart.
        # Revalidate the live executable/arguments, fingerprint stable definitions.
        return digest({"definitions": definitions, "argv": args, "cwd": self.cwd,
                       "binary_sha256": self.binary_digest(binary)})

    def binary_digest(self, binary):
        s = binary.stat()
        if s.st_mode & 0o022 or s.st_uid not in (0, os.geteuid()):
            raise Refused("unsafe_proxy_binary_permissions")
        h = hashlib.sha256()
        with binary.open("rb") as f:
            for raw in iter(lambda: f.read(65536), b""):
                h.update(raw)
        return h.hexdigest()

    def admin_request(self, method="GET", config=None):
        if self.admin.startswith("unix/"):
            conn = UnixHTTP(self.admin[5:], timeout=4)
        else:
            conn = http.client.HTTPConnection(self.admin, timeout=4)
        try:
            raw = None if config is None else json.dumps(config, separators=(",", ":")).encode()
            conn.request(method, "/config/" if method == "GET" else "/load", body=raw,
                         headers={"Content-Type": "application/json", "Host": "localhost"})
            response = conn.getresponse()
            body = response.read(LIMIT + 1)
            if response.status != 200 or len(body) > LIMIT:
                raise Refused("proxy_admin_unavailable_or_oversized")
            return json.loads(body) if method == "GET" else None
        finally:
            conn.close()

    def adapt(self, source=None, data=None):
        args = [self.spec["binary"], "adapt", "--adapter", "caddyfile", "--config", source or "-"]
        return json.loads(command(args, data=data, cwd=self.cwd))

    def source_config(self):
        raw = read_file(self.spec["config"])
        conf = json.loads(raw) if self.spec["format"] == "json" else self.adapt(self.spec["config"])
        return raw, conf

    def locate(self, conf):
        from fnmatch import fnmatchcase
        found = []
        servers = conf.get("apps", {}).get("http", {}).get("servers", {})
        if not isinstance(servers, dict) or len(servers) > 64:
            raise Refused("unsupported_proxy_http_configuration")
        for name, server in servers.items():
            for index, route in enumerate(server.get("routes", [])):
                matches = route.get("match", [])
                if not matches:
                    raise Refused("ambiguous_unscoped_proxy_route")
                for match in matches:
                    hosts = match.get("host", [])
                    if not hosts:
                        raise Refused("ambiguous_unscoped_proxy_route")
                    if self.host in hosts:
                        if matches != [{"host": [self.host]}]:
                            raise Refused("hostname_route_cannot_be_isolated")
                        found.append((name, index))
                    elif any(fnmatchcase(self.host, h) for h in hosts):
                        raise Refused("overlapping_wildcard_hostname")
        if not found or len({a for a, _ in found}) != 1:
            raise Refused("hostname_not_uniquely_owned")
        return found

    def owned_upstreams(self, conf):
        found = self.locate(conf)
        proxies = []
        def walk(obj):
            if isinstance(obj, dict):
                if obj.get("handler") == "reverse_proxy":
                    proxies.append(obj)
                for value in obj.values():
                    walk(value)
            elif isinstance(obj, list):
                for value in obj:
                    walk(value)
        routes = conf["apps"]["http"]["servers"][found[0][0]]["routes"]
        for _, index in found:
            walk(routes[index])
        expected = "127.0.0.1:" + str(self.p["control_port"])
        if not proxies or any(p.get("upstreams") != [{"dial": expected}] or "dynamic_upstreams" in p
                              or p.get("transport", {}).get("protocol", "http") != "http" for p in proxies):
            raise Refused("proxy_upstream_does_not_belong_to_selected_deployment")

    def without_host(self, conf):
        obj = copy.deepcopy(conf)
        found = self.locate(conf)
        name = found[0][0]
        indices = {i for _, i in found}
        routes = obj["apps"]["http"]["servers"][name]["routes"]
        obj["apps"]["http"]["servers"][name]["routes"] = [r for i, r in enumerate(routes) if i not in indices]
        return obj

    def desired(self, conf, publish=True):
        site = self.p["site"] if publish else self.host + ' {\n respond "Not found" 404\n}\n'
        adapted = self.adapt(data=site.encode())
        target = self.locate(adapted)
        replacement = adapted["apps"]["http"]["servers"][target[0][0]]["routes"]
        obj = copy.deepcopy(conf)
        old = self.locate(conf)
        name, first = old[0]
        indices = {i for _, i in old}
        routes = obj["apps"]["http"]["servers"][name]["routes"]
        obj["apps"]["http"]["servers"][name]["routes"] = [r for i, r in enumerate(routes) if i not in indices]
        obj["apps"]["http"]["servers"][name]["routes"][first:first] = replacement
        return site.encode() if self.spec["format"] == "caddyfile" else json.dumps(obj, indent=2).encode(), obj

    def tls_probe(self, expected=None):
        # Connect locally with the public hostname as SNI; verify the normal trust
        # chain. This neither changes DNS nor disables TLS verification.
        context = ssl.create_default_context()
        with socket.create_connection(("127.0.0.1", 443), timeout=4) as raw:
            with context.wrap_socket(raw, server_hostname=self.host) as tls:
                cert = tls.getpeercert()
                fingerprint = hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()
                if expected is not None:
                    tls.sendall(("GET /.well-known/apteva-runtime-ready HTTP/1.1\r\nHost: " + self.host + "\r\nConnection: close\r\n\r\n").encode())
                    response = http.client.HTTPResponse(tls)
                    response.begin()
                    if response.status != 200 or response.read(256).strip().decode() != expected:
                        raise Refused("published_deployment_marker_mismatch")
                return {"verified": True, "sha256": fingerprint, "not_after": cert.get("notAfter")}

    def connections(self):
        raw = command(["ss", "-Hnt", "state", "established", "( sport = :80 or sport = :443 )"])
        return len(raw.decode().strip().splitlines()) if raw.strip() else 0

    def listeners(self):
        raw = command(["ss", "-H", "-lntp", "( sport = :80 or sport = :443 )"])
        ports = []
        for row in raw.decode().splitlines():
            fields = row.split()
            if len(fields)>3 and re.search(r"pid=" + re.escape(self.proxy_pid) + r"\b", row):
                port = fields[3].rsplit(":",1)[-1]
                if port.isdigit(): ports.append(int(port))
        if 443 not in ports:
            raise Refused("selected_proxy_does_not_own_https_listener")
        return sorted(set(ports))

    def require_idle(self, attestation=True):
        if attestation and self.r.get("maintenance_window") is not True:
            raise Refused("externally_quiesced_maintenance_window_required")
        for _ in range(2):
            if self.connections():
                raise Refused("active_connections_require_drain_or_idle_window")
            time.sleep(.1)

    def owner(self):
        if os.path.lexists(self.owner_file):
            if json.loads(read_file(self.owner_file)) != self.identity:
                raise Refused("proxy_owned_by_another_installation_or_controller")

    def state(self):
        if not os.path.lexists(self.journal):
            return None
        state = json.loads(read_file(self.journal))
        if state["identity"] != self.identity:
            raise Refused("proxy_journal_ownership_conflict")
        return state

    def save(self, state):
        atomic_file(self.journal, json.dumps(state, separators=(",", ":")).encode())

    def inspect(self, already_owned=False, publish=True):
        self.owner()
        service = self.service()
        ports = self.listeners()
        source = read_file(self.source)
        main, conf = self.source_config()
        live = self.admin_request()
        if digest(live) != digest(conf):
            raise Refused("live_proxy_differs_from_persistent_configuration")
        listen = conf.get("admin", {}).get("listen", "localhost:2019")
        if listen != self.admin or conf.get("admin", {}).get("disabled"):
            raise Refused("proxy_admin_address_mismatch")
        if not already_owned:
            self.owned_upstreams(conf)
        if self.spec["format"] == "caddyfile":
            fragment = self.adapt(self.source)
            selected = self.locate(fragment)
            # A fragment with another site, global options or explicit TLS policies
            # cannot be silently rewritten. The full-config comparison below is
            # also enforced before the running configuration is touched.
            servers = fragment.get("apps", {}).get("http", {}).get("servers", {})
            count = sum(len(s.get("routes", [])) for s in servers.values())
            if count != len(selected) or set(fragment) != {"apps"} or set(fragment["apps"]) != {"http"}:
                raise Refused("site_file_contains_unrelated_or_global_configuration")
        candidate, target = self.desired(conf, publish)
        tls = self.tls_probe()
        plan = {"identity": self.identity, "deployment_id": self.p["deployment_id"], "generation": self.p["generation"],
                "source_sha256": digest(source), "main_sha256": digest(main), "live_sha256": digest(live),
                "candidate_sha256": digest(target), "service_sha256": service}
        return {"plan_sha256": digest(plan), "observed_at": time.time(), "adapter": "caddy", "hostname": self.host,
                "service": self.spec["service"], "listening_ports": ports, "active_connections": self.connections(),
                "tls": tls, "declared_routes": [r["path"] for r in self.p["routes"]],
                "maintenance_window_required": True, "source_sha256": digest(source), "candidate_sha256": digest(target),
                "replacement_scope": "selected_hostname_handlers", "other_hosts_and_tls": "preserved",
                "authentication_and_cors": "application_owned", "connection_policy": "externally_quiesced_and_idle_only",
                "live_sha256": digest(live), "service_sha256": service}, source, conf, candidate, target

    def verify_live(self, expected):
        if digest(self.admin_request()) != digest(expected):
            raise Refused("proxy_reload_not_confirmed")

    def apply_config(self, raw, expected, metadata):
        atomic_file(self.source, raw, metadata)
        _, actual = self.source_config()
        if digest(actual) != digest(expected):
            raise Refused("candidate_changes_unrelated_routes_tls_or_proxy_settings")
        # Caddy validates/provisions the complete config before activating it; its
        # /load operation retains the old running config on validation failure.
        self.admin_request("POST", expected)
        self.verify_live(expected)

    def restore_previous(self, state):
        source = read_file(self.source)
        live = self.admin_request()
        if digest(source) not in (state["previous_source_sha256"], state["next_source_sha256"]) or digest(live) not in (digest(state["previous_config"]), digest(state["next_config"])):
            raise Refused("rollback_conflicts_with_external_proxy_changes")
        self.apply_config(base64.b64decode(state["previous_source"]), state["previous_config"], state["metadata"])
        state["phase"] = "rolled_back"
        self.save(state)

    def require_fresh_owner(self):
        expected=pathlib.Path(self.p["root"])/"hosts"/(self.host+".caddy")
        unit=pathlib.Path("/etc/systemd/system")/self.spec["service"]
        if str(expected)!=self.source or read_file(str(pathlib.Path(self.p["root"])/(self.host+".owner"))).decode()!=str(self.p["install_id"]) or not read_file(str(unit)).decode().startswith("# apteva-ingress:"+self.p["root"]+"\n"):
            raise Refused("fresh_proxy_ownership_not_established")

    def run(self):
        action = self.p["action"]
        if action=="release" and os.path.lexists(self.journal+".released"):
            prior=json.loads(read_file(self.journal+".released"))
            if prior["identity"]==self.identity and prior["deployment_id"]==self.p["deployment_id"] and prior["generation"]==self.p["generation"]:
                return {"phase":"released","unpublished":True,"reused":True}
        if action == "inspect":
            state = self.state()
            owned = bool(state and state["phase"] in ("active","prepared"))
            if owned:
                self.owner()
                if digest(read_file(self.source)) != state["next_source_sha256"]:
                    raise Refused("managed_configuration_modified_externally")
                self.verify_live(state["next_config"])
            result, *_ = self.inspect(already_owned=owned)
            result["phase"] = state["phase"] if state else "unmanaged"
            return result
        if action == "status":
            self.owner()
            state = self.state()
            if state is None:
                raise Refused("proxy_not_adopted")
            self.service()
            _, conf = self.source_config()
            self.verify_live(conf)
            wanted_source = digest(base64.b64decode(state["original_source"])) if state["phase"]=="released" else state.get("next_source_sha256")
            wanted_config = state["original_config"] if state["phase"]=="released" else state["next_config"]
            if digest(read_file(self.source)) != wanted_source or digest(conf)!=digest(wanted_config):
                raise Refused("managed_configuration_modified_externally")
            return {"phase": state["phase"], "deployment_id": state["deployment_id"], "generation":state["generation"], "plan_sha256":state["plan_sha256"], "source_matches": True, "observed_at": time.time()}
        if action not in ("prepare","adopt", "publish", "unpublish", "rollback","release"):
            raise Refused("unsupported_proxy_operation")
        self.owner()
        # A lock next to the selected source coordinates all hostnames/controllers
        # using this same file. It is created only by explicit mutation operations.
        lockname = self.spec["config"] + ".apteva-lock"
        safe_path(lockname, missing=True)
        lockfd = os.open(lockname, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            try:
                fcntl.flock(lockfd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise Refused("proxy_operation_in_progress")
            self.owner()
            state = self.state()
            service = self.service()
            if state and service != state["service_sha256"] and action != "adopt":
                raise Refused("proxy_service_or_binary_changed_since_adoption")
            if action=="release":
                if not state or state["phase"]!="active" or state.get("published",True) or state["deployment_id"]!=self.p["deployment_id"] or state["generation"]!=self.p["generation"]:
                    raise Refused("release_requires_current_unpublished_proxy")
                if digest(read_file(self.source))!=state["next_source_sha256"]:
                    raise Refused("managed_configuration_modified_externally")
                self.verify_live(state["next_config"])
                atomic_file(self.journal+".released",json.dumps(state).encode())
                os.unlink(self.owner_file);os.unlink(self.journal)
                scalar=pathlib.Path(self.p["root"])/(self.host+".owner")
                if self.source==str(pathlib.Path(self.p["root"])/"hosts"/(self.host+".caddy")) and scalar.exists() and read_file(str(scalar)).decode()==str(self.p["install_id"]):
                    scalar.unlink()
                return {"phase":"released","unpublished":True,"configuration_retained":True}
            if action=="prepare":
                self.require_fresh_owner()
                if state:
                    if state["phase"] not in ("prepared","active"):raise Refused("proxy_preparation_requires_reconciliation")
                    return {"phase":"prepared","reused":True}
                source=read_file(self.source);_,conf=self.source_config();self.verify_live(conf)
                st=os.stat(self.source)
                state={"identity":self.identity,"phase":"prepared","published":False,"deployment_id":self.p["deployment_id"],"generation":self.p["generation"],"plan_sha256":"","service_sha256":service,"metadata":[stat.S_IMODE(st.st_mode),st.st_uid,st.st_gid],"original_source":base64.b64encode(source).decode(),"original_config":conf,"next_source_sha256":digest(source),"next_config":conf}
                self.save(state);atomic_file(self.owner_file,json.dumps(self.identity).encode())
                return {"phase":"prepared","owned":True}
            if action == "rollback":
                if not state:
                    raise Refused("no_owned_proxy_adoption")
                if state["deployment_id"] != self.p["deployment_id"]:
                    raise Refused("rollback_requires_current_deployment_owner")
                self.require_idle()
                if state["phase"] == "released":
                    return {"phase": "released", "reused": True}
                # Explicit release restores the original pre-adoption source/config.
                original = copy.deepcopy(state)
                original["previous_source"] = state["original_source"]
                original["previous_source_sha256"] = digest(base64.b64decode(state["original_source"]))
                original["previous_config"] = state["original_config"]
                self.restore_previous(original)
                state["phase"] = "released"
                self.save(state)
                os.unlink(self.owner_file)
                return {"phase": "released", "rollback_verified": True}
            if state and state["phase"] == "pending":
                if state["deployment_id"] != self.p["deployment_id"] or state["generation"] != self.p["generation"]:
                    raise Refused("unfinished_proxy_operation_identity_conflict")
                if self.r.get("plan_sha256") != state["plan_sha256"] and action == "adopt":
                    raise Refused("unfinished_proxy_operation_requires_original_plan")
                self.require_idle()
                source, live = read_file(self.source), self.admin_request()
                if digest(source) == state["next_source_sha256"] and digest(live) == digest(state["next_config"]):
                    self.tls_probe(state["deployment_id"] if state.get("published",True) else None)
                    state["phase"] = "active"
                    self.save(state)
                elif digest(source) in (state["previous_source_sha256"], state["next_source_sha256"]) and digest(live) == digest(state["previous_config"]):
                    self.apply_config(base64.b64decode(state["next_source"]), state["next_config"], state["metadata"])
                    self.tls_probe(state["deployment_id"] if state.get("published",True) else None)
                    state["phase"] = "active"
                    self.save(state)
                else:
                    raise Refused("proxy_outcome_unknown_requires_manual_reconciliation")
            if action == "adopt" and state and state["phase"] == "active" and self.r.get("plan_sha256") == state["plan_sha256"]:
                if state["deployment_id"] != self.p["deployment_id"] or state["generation"] != self.p["generation"]:
                    raise Refused("adoption_retry_identity_conflict")
                if digest(read_file(self.source)) != state["next_source_sha256"]:
                    raise Refused("managed_configuration_modified_externally")
                self.verify_live(state["next_config"])
                return {"phase": "active", "reused": True, "plan_sha256": state["plan_sha256"]}
            if action != "adopt" and (not state or state["phase"] not in ("active","prepared")):
                raise Refused("proxy_adoption_not_active")
            if state and state["phase"] in ("active","prepared"):
                expected=self.p.get("expected_deployment_id")
                cas=expected==state["deployment_id"] and self.p.get("expected_generation")==state["generation"]
                if action=="publish" and expected and not cas and self.p["deployment_id"]!=state["deployment_id"]:
                    raise Refused("proxy_publication_owner_changed")
                if self.p["generation"] < state["generation"] and action != "unpublish" and not cas:
                    raise Refused("superseded_deployment_cannot_publish_proxy")
                if self.p["generation"] == state["generation"] and self.p["deployment_id"] != state["deployment_id"] and not cas:
                    raise Refused("conflicting_deployment_generation")
                if digest(read_file(self.source)) != state["next_source_sha256"]:
                    raise Refused("managed_configuration_modified_externally")
                self.verify_live(state["next_config"])
                if action == "unpublish" and state["deployment_id"] != self.p["deployment_id"]:
                    return {"phase": "active", "newer_deployment_preserved": True}
            info, source, conf, candidate, target = self.inspect(already_owned=bool(state and state["phase"] in ("active","prepared")), publish=action != "unpublish")
            if action in ("adopt","publish") and self.r.get("plan_sha256") != info["plan_sha256"]:
                raise Refused("reviewed_proxy_plan_is_stale")
            if action == "publish" and digest(conf) == digest(target):
                return {"phase": "active", "reused": True}
            # No restart or unbounded draining: a controller/operator must quiesce
            # arrivals and drain calls first. A quiet instant alone is not proof.
            self.require_idle(attestation=action != "unpublish")
            s = os.stat(self.source)
            original_source = state["original_source"] if state else base64.b64encode(source).decode()
            original_config = state["original_config"] if state else conf
            state = {"identity": self.identity, "phase": "pending", "published":action!="unpublish", "deployment_id": self.p["deployment_id"], "generation": self.p["generation"],
                     "plan_sha256": info["plan_sha256"], "service_sha256": service, "metadata": [stat.S_IMODE(s.st_mode), s.st_uid, s.st_gid],
                     "original_source": original_source, "original_config": original_config,
                     "previous_source": base64.b64encode(source).decode(), "previous_source_sha256": digest(source), "previous_config": conf,
                     "next_source": base64.b64encode(candidate).decode(), "next_source_sha256": digest(candidate), "next_config": target}
            self.save(state)
            atomic_file(self.owner_file, json.dumps(self.identity).encode())
            try:
                self.apply_config(candidate, target, state["metadata"])
                self.tls_probe(self.p["deployment_id"] if action != "unpublish" else None)
            except Exception:
                self.restore_previous(state)
                raise Refused("publication_failed_previous_configuration_restored")
            state["phase"] = "active"
            self.save(state)
            return {"phase": "active", "verified": True, "plan_sha256": info["plan_sha256"], "deployment_id": self.p["deployment_id"], "rollback_available": True}
        finally:
            os.close(lockfd)


def main():
    try:
        payload = json.loads(base64.b64decode(sys.argv[1], validate=True))
        result = Proxy(payload).run()
    except Refused as e:
        result = {"error": str(e)}
    except Exception:
        # Configuration, environment, certificate keys, subprocess output and
        # credentials never enter responses or logs.
        result = {"error": "proxy_evidence_unavailable_or_operation_unconfirmed"}
    print(json.dumps(result))


if __name__ == "__main__":
    main()
