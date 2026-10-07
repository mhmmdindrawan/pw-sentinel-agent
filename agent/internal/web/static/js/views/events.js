// Event search across all gates. Filters live in the URL hash, so a
// filtered view can be shared and Back returns to it.

import { h } from "../core/dom.js";
import { eventList } from "../ui/events.js";

export function mount(root, params, ctx) {
  const list = eventList({ showDevice: true, params, syncUrl: true, onAuthError: ctx.onAuthError });
  root.replaceChildren(h("div", { class: "pagehead" }, h("h1", { tabindex: "-1", text: "Event" })), list.el);
  return { poller: { refresh: list.reload }, destroy: list.destroy };
}
