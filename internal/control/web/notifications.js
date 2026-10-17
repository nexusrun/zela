"use strict";
let notificationCenterOpen = false;
let seenNotifications = null;
const desktopInstances = new Set();
function desktopEnabled() {
  try {
    return localStorage.getItem("zela.desktopNotifications") === "enabled";
  } catch {
    return false;
  }
}
function desktopSupport() {
  if (typeof window.Notification !== "function")
    return "This browser does not support desktop notifications.";
  if (window.isSecureContext === false)
    return "Use HTTPS or localhost to enable desktop notifications.";
  if (window.Notification.permission === "denied")
    return "Notifications are blocked. Allow them in your browser's site settings.";
  return "";
}
function notificationItems() {
  return state?.notifications || [];
}
function unreadNotificationCount() {
  return notificationItems().filter((n) => !n.readAt).length;
}
function notificationBell() {
  const count = unreadNotificationCount();
  return `<button class="notification-bell" data-action="notifications" aria-label="Notifications${count ? `, ${count} unread` : ", none unread"}" aria-haspopup="dialog">${icon("bell")}${count ? `<span class="notification-count">${count > 99 ? "99+" : count}</span>` : ""}</button>`;
}
function desktopControls() {
  const reason = desktopSupport();
  const enabled =
    desktopEnabled() && !reason && window.Notification.permission === "granted";
  return `<div class="desktop-notification-controls"><div><b>Desktop notifications</b><p>${esc(reason || (enabled ? "Enabled for this browser while the console is open." : "Get cluster alerts and operation results while this console is open. Browser permission is required."))}</p></div>${enabled ? btn("Disable", "disable-desktop-notifications", "", "", "bell") : btn("Enable", "enable-desktop-notifications", reason ? "disabled" : "", "primary", "bell")}</div>`;
}
function notificationList() {
  const items = notificationItems();
  if (!items.length)
    return empty(
      "No notifications yet",
      "Worker and resource alerts, recovery events, and operation results will appear here.",
    );
  return `<div class="notification-list">${items.map((n) => `<article class="notification-item ${n.readAt ? "" : "unread"}"><div class="activity-icon ${n.severity === "critical" ? "failed" : n.kind === "alert-resolved" ? "succeeded" : ""}">${icon(n.kind === "alert-resolved" || n.kind === "operation-succeeded" ? "check" : "bell")}</div><div class="notification-content"><div class="notification-title">${esc(n.title)}${!n.readAt ? '<span class="notification-unread-dot" aria-label="Unread"></span>' : ""}</div><p>${esc(n.message)}</p><div class="small-sub">${esc(n.severity)} · ${relative(n.createdAt)}</div><div class="notification-actions">${btn(n.operationID ? "View operation" : n.containerID ? (n.kind === "container-deleted" ? "View containers" : "View container") : "View alert", "open-notification", `data-id="${esc(n.id)}"`, "text", "arrow")}${!n.readAt ? btn("Mark read", "read-notification", `data-id="${esc(n.id)}"`, "text", "check") : ""}</div></div></article>`).join("")}</div>`;
}
function showNotificationCenter() {
  openModal(
    "Notifications",
    "Live cluster events. Read status is shared by administrators; reading an alert does not acknowledge or resolve it.",
    `<div id="notification-center-content">${notificationCenterContent()}</div>`,
    btn("Close", "close"),
    "",
    true,
  );
  notificationCenterOpen = true;
}
function notificationCenterContent() {
  return `${desktopControls()}<div class="notification-toolbar"><b>${unreadNotificationCount()} unread</b>${btn("Mark all read", "read-all-notifications", unreadNotificationCount() ? "" : "disabled", "", "check")}</div>${notificationList()}`;
}
function refreshNotificationCenter() {
  const content = $("#notification-center-content");
  if (notificationCenterOpen && content)
    content.innerHTML = notificationCenterContent();
}
async function readNotifications(ids) {
  if (!ids.length) return;
  const result = await api("/notifications/read", { ids });
  if (!state) return;
  state.notifications = result.notifications;
  state.unreadNotifications = result.unread;
  render();
  refreshNotificationCenter();
}
async function openNotification(id) {
  const n = notificationItems().find((n) => n.id === id);
  if (!n) return;
  if (!n.readAt) await readNotifications([id]);
  if (!state) return;
  if (n.operationID) {
    navigate("activity");
    if (state.operations.some((op) => op.id === n.operationID))
      operationDetails(n.operationID);
    else toast("This operation's detailed history is no longer retained.");
  } else if (n.containerID) {
    navigate("containers");
    if (
      state.workers[n.workerID]?.containers?.some(
        (ct) => ct.Id === n.containerID,
      )
    )
      containerDetails(n.workerID, n.containerID);
    else if (n.kind !== "container-deleted")
      toast("Container inventory updates at the next worker poll.");
  } else {
    navigate("alerts");
    if (!state.alerts?.some((a) => a.id === n.alertID))
      toast("This alert's detailed history is no longer retained.");
  }
}
function deliverDesktopNotification(n) {
  if (
    !desktopEnabled() ||
    desktopSupport() ||
    window.Notification.permission !== "granted"
  )
    return;
  try {
    const popup = new window.Notification(`Zela · ${n.title}`, {
      body: n.message,
      icon: "/favicon.svg",
      tag: n.id,
    });
    desktopInstances.add(popup);
    popup.onclose = () => desktopInstances.delete(popup);
    popup.onclick = () => {
      popup.close();
      window.focus();
      void openNotification(n.id).catch((e) => toast(e.message, true));
    };
    if (desktopInstances.size > 20) {
      const oldest = desktopInstances.values().next().value;
      oldest.close();
      desktopInstances.delete(oldest);
    }
  } catch {
    /* The persisted inbox remains available if the OS refuses delivery. */
  }
}
function processNotifications() {
  const items = notificationItems();
  // Establish a baseline on sign-in/reload instead of replaying old events.
  if (seenNotifications === null) {
    seenNotifications = new Set(items.map((n) => n.id));
    return;
  }
  const fresh = items.filter((n) => !seenNotifications.has(n.id) && !n.readAt);
  for (const n of items) seenNotifications.add(n.id);
  while (seenNotifications.size > 1000)
    seenNotifications.delete(seenNotifications.values().next().value);
  if (fresh.length) {
    const ordered = [...fresh].sort(
      (a, b) => new Date(a.createdAt) - new Date(b.createdAt),
    );
    // Limit desktop bursts after reconnecting; every event remains in the inbox.
    ordered.slice(-5).forEach(deliverDesktopNotification);
    toast(
      fresh.length === 1
        ? `${fresh[0].title} · ${fresh[0].message}`
        : `${fresh.length} new cluster notifications`,
      fresh.some((n) => n.severity === "critical"),
    );
  }
}
async function enableDesktopNotifications() {
  const reason = desktopSupport();
  if (reason) throw new Error(reason);
  const permission = await window.Notification.requestPermission();
  if (permission !== "granted") {
    toast(
      "Desktop permission was not granted. Cluster notifications remain available in the bell.",
      true,
    );
    refreshNotificationCenter();
    return;
  }
  try {
    localStorage.setItem("zela.desktopNotifications", "enabled");
  } catch {
    throw new Error(
      "This browser cannot save notification preferences. Allow site storage and retry.",
    );
  }
  refreshNotificationCenter();
  if (page === "settings") render();
  deliverDesktopNotification({
    id: "desktop-enabled",
    title: "Notifications enabled",
    message:
      "This browser will notify you about new cluster alerts and operation results.",
  });
  toast("Desktop notifications enabled");
}
function disableDesktopNotifications() {
  try {
    localStorage.removeItem("zela.desktopNotifications");
  } catch {
    throw new Error("Unable to save notification preference.");
  }
  closeDesktopNotifications();
  refreshNotificationCenter();
  if (page === "settings") render();
  toast("Desktop notifications disabled for this browser");
}
function closeDesktopNotifications() {
  for (const popup of desktopInstances) {
    popup.onclick = null;
    popup.close();
  }
  desktopInstances.clear();
}
function resetNotifications() {
  seenNotifications = null;
  closeDesktopNotifications();
}
function notificationsSettingsPanel() {
  return `<section class="panel"><div class="panel-head"><h2>Notifications</h2>${icon("bell")}</div><div class="settings-body"><p>Worker failures, sustained CPU/memory/disk breaches, unhealthy containers, deployment degradation and recoveries create real notifications. Completed and failed console operations and successful Docker gateway container changes appear here too.</p>${desktopControls()}<p>Use the bell for persistent history and unread status. Notifications are retained for seven days, up to 500 events. Desktop delivery works while this console is open and requires browser permission; it is not background push delivery.</p>${btn("Open notification center", "notifications", "", "", "bell")}</div></section>`;
}
