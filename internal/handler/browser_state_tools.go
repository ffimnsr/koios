package handler

import (
	"fmt"
	"strings"
)

type browserViewportSpec struct {
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DeviceScaleFactor float64 `json:"device_scale_factor"`
	Mobile            bool    `json:"mobile"`
	Touch             bool    `json:"touch"`
	Landscape         bool    `json:"landscape"`
}

func buildBrowserCookieListFunction() string {
	return `(input) => {
		const decode = (value) => {
			try {
				return decodeURIComponent(value);
			} catch {
				return value;
			}
		};
		const raw = document.cookie || "";
		if (!raw) {
			return { marker: "koios:cookies:list", cookies: [] };
		}
		const cookies = raw.split(/;\s*/).filter(Boolean).map((entry) => {
			const index = entry.indexOf("=");
			const name = index === -1 ? entry : entry.slice(0, index);
			const value = index === -1 ? "" : entry.slice(index + 1);
			return { name: decode(name), value: decode(value) };
		});
		return { marker: "koios:cookies:list", cookies };
	}`
}

func buildBrowserCookieSetFunction() string {
	return `(input) => {
		const encode = (value) => encodeURIComponent(String(value ?? ""));
		const parts = [encode(input?.name ?? "") + "=" + encode(input?.value ?? "")];
		if (input?.path) {
			parts.push("Path=" + input.path);
		}
		if (input?.domain) {
			parts.push("Domain=" + input.domain);
		}
		if (input?.expires) {
			parts.push("Expires=" + input.expires);
		}
		if (input?.maxAge !== undefined && input?.maxAge !== null) {
			parts.push("Max-Age=" + Math.trunc(input.maxAge));
		}
		if (input?.sameSite) {
			parts.push("SameSite=" + input.sameSite);
		}
		if (input?.secure) {
			parts.push("Secure");
		}
		document.cookie = parts.join("; ");
		return { marker: "koios:cookies:set", ok: true, name: input?.name ?? "" };
	}`
}

func buildBrowserCookieDeleteFunction() string {
	return `(input) => {
		const encode = (value) => encodeURIComponent(String(value ?? ""));
		const parts = [encode(input?.name ?? "") + "="];
		parts.push("Expires=Thu, 01 Jan 1970 00:00:00 GMT");
		parts.push("Max-Age=0");
		if (input?.path) {
			parts.push("Path=" + input.path);
		}
		if (input?.domain) {
			parts.push("Domain=" + input.domain);
		}
		document.cookie = parts.join("; ");
		return { marker: "koios:cookies:delete", ok: true, name: input?.name ?? "" };
	}`
}

func buildBrowserStorageInspectFunction() string {
	return `(input) => {
		const scopes = Array.isArray(input?.scopes) && input.scopes.length > 0 ? input.scopes : ["local", "session"];
		const keys = Array.isArray(input?.keys) && input.keys.length > 0 ? new Set(input.keys) : null;
		const dump = (store) => {
			const entries = [];
			for (let index = 0; index < store.length; index += 1) {
				const key = store.key(index);
				if (key === null) {
					continue;
				}
				if (keys && !keys.has(key)) {
					continue;
				}
				entries.push({ key, value: store.getItem(key) });
			}
			return entries;
		};
		const result = { marker: "koios:storage:inspect" };
		if (scopes.includes("local")) {
			result.local = dump(window.localStorage);
		}
		if (scopes.includes("session")) {
			result.session = dump(window.sessionStorage);
		}
		return result;
	}`
}

func buildBrowserStorageSetFunction() string {
	return `(input) => {
		const scope = input?.scope === "session" ? window.sessionStorage : window.localStorage;
		scope.setItem(String(input?.key ?? ""), String(input?.value ?? ""));
		return { marker: "koios:storage:set", ok: true, scope: input?.scope ?? "local", key: input?.key ?? "" };
	}`
}

func buildBrowserStorageDeleteFunction() string {
	return `(input) => {
		const scope = input?.scope === "session" ? window.sessionStorage : window.localStorage;
		scope.removeItem(String(input?.key ?? ""));
		return { marker: "koios:storage:delete", ok: true, scope: input?.scope ?? "local", key: input?.key ?? "" };
	}`
}

func buildBrowserStorageClearFunction() string {
	return `(input) => {
		const scopes = Array.isArray(input?.scopes) && input.scopes.length > 0 ? input.scopes : ["local", "session"];
		if (scopes.includes("local")) {
			window.localStorage.clear();
		}
		if (scopes.includes("session")) {
			window.sessionStorage.clear();
		}
		return { marker: "koios:storage:clear", ok: true, scopes };
	}`
}

func buildBrowserApplyEmulationFunction() string {
	return `(input) => {
		const root = globalThis;
		const state = root.__koiosBrowserEmulation || (root.__koiosBrowserEmulation = {
			installed: false,
			config: { extraHeaders: {} },
			watchId: 1,
		});
		const has = (key) => Object.prototype.hasOwnProperty.call(input || {}, key);
		if (input?.reset) {
			state.config = { extraHeaders: {} };
		}
		if (has("offline")) {
			state.config.offline = !!input.offline;
		}
		if (has("timezone")) {
			state.config.timezone = input.timezone || "";
		}
		if (has("userAgent")) {
			state.config.userAgent = input.userAgent || "";
		}
		if (has("geolocation")) {
			state.config.geolocation = input.geolocation || null;
		}
		if (has("extraHeaders")) {
			state.config.extraHeaders = input.extraHeaders && typeof input.extraHeaders === "object" ? input.extraHeaders : {};
		}
		if (!state.installed) {
			state.installed = true;
			if (typeof root.fetch === "function") {
				state.originalFetch = root.fetch.bind(root);
				root.fetch = async (resource, init = {}) => {
					const cfg = root.__koiosBrowserEmulation?.config || {};
					if (cfg.offline) {
						throw new TypeError("Network is offline by browser.emulate");
					}
					const headers = new Headers(init?.headers || (resource && resource.headers) || undefined);
					for (const [key, value] of Object.entries(cfg.extraHeaders || {})) {
						headers.set(key, String(value));
					}
					return state.originalFetch(resource, { ...init, headers });
				};
			}
			if (typeof root.XMLHttpRequest === "function") {
				const originalOpen = root.XMLHttpRequest.prototype.open;
				const originalSend = root.XMLHttpRequest.prototype.send;
				const originalSetRequestHeader = root.XMLHttpRequest.prototype.setRequestHeader;
				root.XMLHttpRequest.prototype.open = function(...args) {
					this.__koiosHeaders = {};
					return originalOpen.apply(this, args);
				};
				root.XMLHttpRequest.prototype.setRequestHeader = function(key, value) {
					this.__koiosHeaders = this.__koiosHeaders || {};
					this.__koiosHeaders[key] = value;
					return originalSetRequestHeader.call(this, key, value);
				};
				root.XMLHttpRequest.prototype.send = function(...args) {
					const cfg = root.__koiosBrowserEmulation?.config || {};
					if (cfg.offline) {
						throw new Error("Network is offline by browser.emulate");
					}
					for (const [key, value] of Object.entries(cfg.extraHeaders || {})) {
						if (!this.__koiosHeaders || !Object.prototype.hasOwnProperty.call(this.__koiosHeaders, key)) {
							originalSetRequestHeader.call(this, key, String(value));
						}
					}
					return originalSend.apply(this, args);
				};
			}
			try {
				Object.defineProperty(root.navigator, "onLine", {
					configurable: true,
					get() {
						return !(root.__koiosBrowserEmulation?.config?.offline);
					},
				});
			} catch {}
			try {
				const userAgentDescriptor = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(root.navigator), "userAgent");
				const originalUserAgentGetter = userAgentDescriptor?.get ? userAgentDescriptor.get.bind(root.navigator) : null;
				Object.defineProperty(root.navigator, "userAgent", {
					configurable: true,
					get() {
						const override = root.__koiosBrowserEmulation?.config?.userAgent;
						if (override) {
							return override;
						}
						return originalUserAgentGetter ? originalUserAgentGetter() : "";
					},
				});
			} catch {}
			if (root.navigator && root.navigator.geolocation) {
				const geolocation = root.navigator.geolocation;
				const originalGetCurrentPosition = geolocation.getCurrentPosition ? geolocation.getCurrentPosition.bind(geolocation) : null;
				const originalWatchPosition = geolocation.watchPosition ? geolocation.watchPosition.bind(geolocation) : null;
				const originalClearWatch = geolocation.clearWatch ? geolocation.clearWatch.bind(geolocation) : null;
				geolocation.getCurrentPosition = (success, error) => {
					const coords = root.__koiosBrowserEmulation?.config?.geolocation;
					if (!coords) {
						if (originalGetCurrentPosition) {
							return originalGetCurrentPosition(success, error);
						}
						if (typeof error === "function") {
							error({ code: 2, message: "Geolocation unavailable" });
						}
						return undefined;
					}
					if (typeof success === "function") {
						success({
							coords: {
								latitude: Number(coords.latitude),
								longitude: Number(coords.longitude),
								accuracy: 1,
								altitude: null,
								altitudeAccuracy: null,
								heading: null,
								speed: null,
							},
							timestamp: Date.now(),
						});
					}
					return undefined;
				};
				geolocation.watchPosition = (success, error) => {
					geolocation.getCurrentPosition(success, error);
					const id = state.watchId;
					state.watchId += 1;
					return id;
				};
				geolocation.clearWatch = (id) => {
					if (originalClearWatch) {
						return originalClearWatch(id);
					}
					return undefined;
				};
				state.originalGeolocation = { originalWatchPosition, originalGetCurrentPosition, originalClearWatch };
			}
			try {
				const originalResolvedOptions = Intl.DateTimeFormat.prototype.resolvedOptions;
				Intl.DateTimeFormat.prototype.resolvedOptions = function(...args) {
					const result = originalResolvedOptions.apply(this, args);
					const timezone = root.__koiosBrowserEmulation?.config?.timezone;
					if (timezone) {
						result.timeZone = timezone;
					}
					return result;
				};
			} catch {}
		}
		return {
			marker: "koios:emulation:apply",
			offline: !!state.config.offline,
			timezone: state.config.timezone || "",
			userAgent: state.config.userAgent || "",
			geolocation: state.config.geolocation || null,
			extraHeaders: state.config.extraHeaders || {},
		};
	}`
}

func formatBrowserViewport(spec *browserViewportSpec) (string, error) {
	if spec == nil {
		return "", nil
	}
	if spec.Width <= 0 || spec.Height <= 0 {
		return "", fmt.Errorf("viewport width and height must be greater than zero")
	}
	dpr := spec.DeviceScaleFactor
	if dpr <= 0 {
		dpr = 1
	}
	parts := []string{fmt.Sprintf("%dx%dx%g", spec.Width, spec.Height, dpr)}
	if spec.Mobile {
		parts = append(parts, "mobile")
	}
	if spec.Touch {
		parts = append(parts, "touch")
	}
	if spec.Landscape {
		parts = append(parts, "landscape")
	}
	return strings.Join(parts, ","), nil
}

func normalizeBrowserNetworkCondition(offline *bool, profile string) string {
	if offline != nil {
		if *offline {
			return "Offline"
		}
		if strings.TrimSpace(profile) == "" {
			return ""
		}
	}
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "", "none", "off", "reset":
		return ""
	case "offline":
		return "Offline"
	case "slow_3g", "slow 3g":
		return "Slow 3G"
	case "fast_3g", "fast 3g":
		return "Fast 3G"
	case "slow_4g", "slow 4g":
		return "Slow 4G"
	case "fast_4g", "fast 4g":
		return "Fast 4G"
	default:
		return strings.TrimSpace(profile)
	}
}
