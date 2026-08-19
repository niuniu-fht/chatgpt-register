const ADOBE_STATUS = {
  pending: '待生产', registering: '注册中', registered: '已注册', exported: '已出库', register_failed: '注册失败'
};
let adobePage = 1;
let adobePageSize = 20;
let adobeRows = {};
let adobeEditID = null;
let adobeLogID = null;
let adobeLogTimer = null;
const adobeSelected = new Set();
let adobeExportText = '';
let adobeExportIDs = [];
let adobeExportConfirmed = false;

async function loadAdobeAccounts() {
  const params = new URLSearchParams({page: adobePage, size: adobePageSize});
  const q = document.getElementById('ad-search').value.trim();
  const status = document.getElementById('ad-status').value;
  if (q) params.set('q', q);
  if (status) params.set('status', status);
  const r = await api('/api/adobe/accounts?' + params);
  const d = await r.json();
  adobeRows = {};
  (d.data || []).forEach(x => { adobeRows[x.id] = x; });
	(d.data || []).forEach(x => { if (x.status !== 'registered') adobeSelected.delete(x.id); });
  document.getElementById('ad-rows').innerHTML = (d.data || []).map(adobeRowHTML).join('') ||
	'<tr><td colspan="7" class="table-empty">暂无 Adobe 账号</td></tr>';
  renderPager('ad-pager', adobePage, Math.max(1, Math.ceil((d.total || 0) / adobePageSize)), p => {
    adobePage = p; loadAdobeAccounts();
  });
	updateAdobeSelection();
}

function adobeRowHTML(x) {
  const profile = [x.birth_year || '-', String(x.birth_month || '-').padStart(2, '0')].join('-') +
    (x.country ? ' / ' + x.country : '');
  const name = [x.first_name, x.last_name].filter(Boolean).join(' ') || '-';
  return `<tr>
	<td class="select-cell"><input type="checkbox" aria-label="选择 ${esc(x.email)}" ${x.status === 'registered' ? '' : 'disabled'} ${adobeSelected.has(x.id) ? 'checked' : ''} onchange="toggleAdobeAccount(${x.id}, this.checked)"></td>
    <td><strong>${esc(x.email)}</strong>${x.note ? `<small class="cell-note">${esc(x.note)}</small>` : ''}</td>
    <td>${esc(name)}</td>
    <td>${esc(profile)}</td>
    <td><span class="badge ${esc(x.status)}">${ADOBE_STATUS[x.status] || esc(x.status)}</span></td>
    <td>${fmtTime(x.updated_at)}</td>
    <td class="action-cell">
      <button class="icon-btn" title="日志" onclick="showAdobeLog(${x.id})"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6M8 13h8M8 17h5"/></svg></button>
      <button class="icon-btn" title="编辑" ${x.status === 'registering' ? 'disabled' : ''} onclick="openAdobeEditor(${x.id})"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4z"/></svg></button>
      <button class="icon-btn" title="重试" ${x.status === 'register_failed' ? '' : 'disabled'} onclick="retryAdobe(${x.id})"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg></button>
      <button class="icon-btn danger" title="停止此账号" ${x.status === 'registering' ? '' : 'disabled'} onclick="stopAdobeAccount(${x.id})"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><rect x="7" y="7" width="10" height="10"/></svg></button>
      <button class="icon-btn danger" title="删除" ${x.status === 'registering' ? 'disabled' : ''} onclick="deleteAdobe(${x.id})"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9"><path d="M3 6h18M8 6V4h8v2m2 0v14H6V6M10 11v6M14 11v6"/></svg></button>
    </td>
  </tr>`;
}

function toggleAdobeAccount(id, checked) {
	if (checked) adobeSelected.add(id); else adobeSelected.delete(id);
	updateAdobeSelection();
}

function toggleAdobePage(checked) {
	Object.values(adobeRows).forEach(x => {
		if (x.status === 'registered') {
			if (checked) adobeSelected.add(x.id); else adobeSelected.delete(x.id);
		}
	});
	document.querySelectorAll('#ad-rows .select-cell input:not(:disabled)').forEach(box => { box.checked = checked; });
	updateAdobeSelection();
}

function updateAdobeSelection() {
	const eligible = Object.values(adobeRows).filter(x => x.status === 'registered');
	const selectedHere = eligible.filter(x => adobeSelected.has(x.id)).length;
	const all = document.getElementById('ad-select-all');
	all.checked = eligible.length > 0 && selectedHere === eligible.length;
	all.indeterminate = selectedHere > 0 && selectedHere < eligible.length;
	const button = document.getElementById('ad-export-btn');
	button.disabled = adobeSelected.size === 0;
	button.textContent = adobeSelected.size ? `导出邮箱 (${adobeSelected.size})` : '导出邮箱';
}

async function exportAdobeAccounts() {
	const ids = Array.from(adobeSelected);
	if (!ids.length) return toast('请选择已注册账号', true);
	const r = await api('/api/adobe/accounts/export/preview', {
		method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ids})
	});
	const d = await r.json().catch(() => ({}));
	if (!r.ok) return toast(d.error || '生成导出预览失败', true);
	adobeExportIDs = ids;
	adobeExportConfirmed = false;
	adobeExportText = d.text || (d.emails || []).join('\n') + '\n';
	document.getElementById('ad-export-title').textContent = `确认导出 ${d.count || ids.length} 个邮箱`;
	document.getElementById('ad-export-sub').textContent = '确认后所选账号状态将更新为“已出库”。';
	document.getElementById('ad-export-text').value = adobeExportText;
	const confirmButton = document.getElementById('ad-export-confirm');
	confirmButton.disabled = false;
	confirmButton.textContent = '确认出库并下载';
	document.getElementById('ad-export-modal').style.display = 'flex';
}

async function confirmAdobeExport() {
	if (adobeExportConfirmed) return downloadAdobeExport();
	if (!adobeExportIDs.length) return toast('导出预览已失效，请重新选择', true);
	const confirmButton = document.getElementById('ad-export-confirm');
	confirmButton.disabled = true;
	const r = await api('/api/adobe/accounts/export', {
		method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({ids:adobeExportIDs})
	});
	const d = await r.json().catch(() => ({}));
	if (!r.ok) {
		confirmButton.disabled = false;
		return toast(d.error || '确认出库失败', true);
	}
	adobeExportText = d.text || (d.emails || []).join('\n') + '\n';
	document.getElementById('ad-export-text').value = adobeExportText;
	document.getElementById('ad-export-title').textContent = `已出库 ${d.count || 0} 个邮箱`;
	document.getElementById('ad-export-sub').textContent = 'TXT 已触发下载，可在关闭前再次下载。';
	adobeExportConfirmed = true;
	adobeExportIDs = [];
	confirmButton.disabled = false;
	confirmButton.textContent = '再次下载 TXT';
	adobeSelected.clear();
	await loadAdobeAccounts();
	downloadAdobeExport();
	toast(`已出库 ${d.count || 0} 个邮箱`);
}

function downloadAdobeExport() {
	if (!adobeExportText) return;
	const blob = new Blob([adobeExportText], {type:'text/plain;charset=utf-8'});
	const url = URL.createObjectURL(blob);
	const link = document.createElement('a');
	const now = new Date();
	const stamp = now.toISOString().replace(/[-:T]/g, '').slice(0, 14);
	link.href = url;
	link.download = `adobe-emails-${stamp}.txt`;
	document.body.appendChild(link);
	link.click();
	link.remove();
	URL.revokeObjectURL(url);
}

async function loadAdobeProduce() {
  const r = await api('/api/adobe/produce/status');
  const s = await r.json();
  document.getElementById('ad-pending').textContent = s.pending || 0;
  document.getElementById('ad-running').textContent = s.running_num || 0;
  document.getElementById('ad-registered').textContent = s.registered || 0;
  document.getElementById('ad-failed').textContent = s.failed || 0;
  document.getElementById('ad-message').textContent = s.message || '';
  document.getElementById('ad-stop').style.display = s.running ? '' : 'none';
  document.getElementById('ad-produce-btn').disabled = !!s.running;
}

function openAdobeEditor(id) {
  adobeEditID = id || null;
  const x = id ? adobeRows[id] : {};
  document.getElementById('ad-edit-title').textContent = id ? '编辑 Adobe 账号' : '添加 Adobe 账号';
  document.getElementById('ad-email').value = x.email || '';
  document.getElementById('ad-password').value = '';
  document.getElementById('ad-first-name').value = x.first_name || '';
  document.getElementById('ad-last-name').value = x.last_name || '';
  document.getElementById('ad-birth-year').value = x.birth_year || 1994;
  document.getElementById('ad-birth-month').value = x.birth_month || 6;
  document.getElementById('ad-country').value = x.country || 'SG';
  document.getElementById('ad-note').value = x.note || '';
  document.getElementById('ad-edit-modal').style.display = 'flex';
}

async function saveAdobeAccount() {
  const payload = {
    email: document.getElementById('ad-email').value.trim(),
    password: document.getElementById('ad-password').value,
    first_name: document.getElementById('ad-first-name').value.trim(),
    last_name: document.getElementById('ad-last-name').value.trim(),
    birth_year: Number(document.getElementById('ad-birth-year').value),
    birth_month: Number(document.getElementById('ad-birth-month').value),
    country: document.getElementById('ad-country').value.trim(),
    note: document.getElementById('ad-note').value.trim()
  };
  if (!payload.email) return toast('请输入邮箱', true);
  const url = adobeEditID ? '/api/adobe/accounts/' + adobeEditID : '/api/adobe/accounts';
  const r = await api(url, {method: adobeEditID ? 'PUT' : 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify(payload)});
  if (!r.ok) return toast((await r.json().catch(() => ({}))).error || '保存失败', true);
  closeModal('ad-edit-modal');
  toast('已保存');
  loadAdobeAccounts();
}

function openAdobeImport() {
  document.getElementById('ad-import-lines').value = '';
  document.getElementById('ad-import-modal').style.display = 'flex';
}

async function importAdobeAccounts() {
  const lines = document.getElementById('ad-import-lines').value.trim();
  if (!lines) return toast('请输入导入内容', true);
  const r = await api('/api/adobe/accounts/import', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({lines})});
  const d = await r.json().catch(() => ({}));
  if (!r.ok) return toast(d.error || '导入失败', true);
  closeModal('ad-import-modal');
  toast(`已导入 ${d.created || 0} 条，跳过 ${d.skipped || 0} 条`);
  loadAdobeAccounts();
}

async function openAdobeProduce() {
  document.getElementById('ad-produce-count').value = 1;
  document.getElementById('ad-produce-concurrency').value = 1;
	const r = await api('/api/settings');
	const settings = await r.json().catch(() => ({}));
	document.getElementById('ad-browser-mode').value = settings.adobe_browser_mode === 'system' ? 'system' : 'cloak';
	document.getElementById('ad-cloak-path').value = settings.adobe_cloak_browser_path || '';
	onAdobeBrowserMode();
  document.getElementById('ad-produce-modal').style.display = 'flex';
}

function onAdobeBrowserMode() {
	const cloak = document.getElementById('ad-browser-mode').value === 'cloak';
	document.getElementById('ad-cloak-path-field').style.display = cloak ? '' : 'none';
	const concurrency = document.getElementById('ad-produce-concurrency');
	if (cloak) concurrency.value = 1;
	concurrency.disabled = cloak;
}

async function startAdobeProduce() {
	const browserMode = document.getElementById('ad-browser-mode').value;
	const browserPath = document.getElementById('ad-cloak-path').value.trim();
	if (browserMode === 'cloak') document.getElementById('ad-produce-concurrency').value = 1;
  const payload = {
    count:Number(document.getElementById('ad-produce-count').value),
    concurrency:Number(document.getElementById('ad-produce-concurrency').value),
	headless:document.getElementById('ad-headless').checked,
	browser_mode:browserMode,
	browser_path:browserPath
  };
	const save = await api('/api/settings', {
		method:'PUT', headers:{'Content-Type':'application/json'},
		body:JSON.stringify({adobe_browser_mode:browserMode, adobe_cloak_browser_path:browserPath})
	});
	if (!save.ok) return toast('浏览器配置保存失败', true);
  const r = await api('/api/adobe/produce', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(payload)});
  const d = await r.json().catch(() => ({}));
  if (!r.ok) return toast(d.error || '启动生产失败', true);
  closeModal('ad-produce-modal');
  toast('Adobe 生产已启动');
  loadAdobeProduce();
}

async function stopAdobeProduce() {
  try {
    const r = await api('/api/adobe/produce/stop', {method:'POST'});
    const d = await r.json().catch(() => ({}));
    if (!r.ok) return toast(d.error || '停止失败', true);
    toast('正在停止 Adobe 生产');
    await Promise.all([loadAdobeProduce(), loadAdobeAccounts()]);
  } catch (e) {
    toast('服务连接失败，请刷新页面后重试', true);
  }
}

async function retryAdobe(id) {
  const r = await api(`/api/adobe/accounts/${id}/retry`, {method:'POST'});
  if (!r.ok) return toast((await r.json().catch(() => ({}))).error || '重试设置失败', true);
  toast('已重新加入待生产');
  loadAdobeAccounts();
}

async function stopAdobeAccount(id) {
  try {
    const r = await api(`/api/adobe/accounts/${id}/stop`, {method:'POST'});
    const d = await r.json().catch(() => ({}));
    if (!r.ok) return toast(d.error || '停止账号失败', true);
    toast('正在停止此账号');
    await Promise.all([loadAdobeAccounts(), loadAdobeProduce()]);
  } catch (e) {
    toast('服务连接失败，请刷新页面后重试', true);
  }
}

async function deleteAdobe(id) {
  if (!confirm('确定删除该 Adobe 账号记录?')) return;
  try {
    const r = await api(`/api/adobe/accounts/${id}`, {method:'DELETE'});
    if (!r.ok) return toast((await r.json().catch(() => ({}))).error || '删除失败', true);
    delete adobeRows[id];
    toast('已删除');
    await loadAdobeAccounts();
  } catch (e) {
    toast('服务连接失败，请刷新页面后重试', true);
  }
}

async function showAdobeLog(id) {
  adobeLogID = id;
  document.getElementById('ad-log-modal').style.display = 'flex';
  document.getElementById('ad-log-body').textContent = '加载中...';
  await refreshAdobeLog();
  clearInterval(adobeLogTimer);
  adobeLogTimer = setInterval(refreshAdobeLog, 1800);
}

async function refreshAdobeLog() {
  if (!adobeLogID) return;
  const r = await api(`/api/adobe/accounts/${adobeLogID}/logs`);
  if (!r.ok) return;
  const d = await r.json();
  document.getElementById('ad-log-title').textContent = '执行日志 · ' + d.email;
  document.getElementById('ad-log-body').textContent = (d.note ? '备注: ' + d.note + '\n\n' : '') + (d.log || '（无执行日志）');
  document.getElementById('ad-shot-btn').style.display = d.has_shot ? '' : 'none';
}

function closeAdobeLog() {
  clearInterval(adobeLogTimer);
  adobeLogTimer = null;
  adobeLogID = null;
  closeModal('ad-log-modal');
}

async function showAdobeShot() {
  if (!adobeLogID) return;
  const r = await api(`/api/adobe/accounts/${adobeLogID}/shot`);
  if (!r.ok) return toast('暂无错误截图', true);
  const blob = await r.blob();
  const img = document.getElementById('ad-shot-img');
  if (img.dataset.url) URL.revokeObjectURL(img.dataset.url);
  img.src = img.dataset.url = URL.createObjectURL(blob);
  document.getElementById('ad-shot-modal').style.display = 'flex';
}

document.getElementById('ad-search').addEventListener('keydown', e => { if (e.key === 'Enter') { adobePage = 1; loadAdobeAccounts(); } });
document.getElementById('ad-status').addEventListener('change', () => { adobePage = 1; loadAdobeAccounts(); });
document.getElementById('ad-page-size').addEventListener('change', e => {
  adobePageSize = Number(e.target.value) || 20;
  adobePage = 1;
  loadAdobeAccounts();
});
loadAdobeAccounts();
loadAdobeProduce();
setInterval(loadAdobeAccounts, 3000);
setInterval(loadAdobeProduce, 1500);
