// 应用的全局脚本：在 head 中加载一次。
window.__bodyScriptRuns = (window.__bodyScriptRuns || 0) + 1; // PoC：检查后退时是否重复执行

// 服务端通过 HX-Trigger: {"dialog:close": {"id": "..."}} 关闭对话框。
document.addEventListener("dialog:close", function (e) {
	window.templ.dialog.close(e.detail.id);
});

// htmx 请求的提示：web.Flash → HX-Trigger → body 上的 web:flash 事件。
document.addEventListener("web:flash", function (e) {
	e.detail.messages.forEach(function (m) { window.templ.toast.add(m); });
});

// templ 会拦截字符串形式的 onclick，这里用事件委托。
document.addEventListener("click", function (e) {
	if (!e.target.closest("[data-theme-toggle]")) return;
	var dark = document.documentElement.classList.toggle("dark");
	try { localStorage.setItem("theme", dark ? "dark" : "light"); } catch (e) {}
});
