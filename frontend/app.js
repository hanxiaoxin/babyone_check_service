const $ = id => document.getElementById(id);
const el = (tag, text, cls) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (cls) n.className = cls; return n; };
const date = seconds => seconds ? new Date(seconds * 1000).toLocaleString() : '—';
const states = {up:'正常', down:'异常', paused:'已暂停', unknown:'等待检测'};
let token = '', projects = [], page = 1, pages = 0, version = 0;
let historyTarget, historyWindow, cursor, historyVersion = 0;
const url = new URL(location.href);
const tokens = url.searchParams.getAll('token');
url.searchParams.delete('token');
history.replaceState(null, '', url.pathname + url.search + url.hash);
function message(text = '', error = false) { $('message').textContent = text; $('message').className = error ? 'error' : ''; }
function disconnect() { token = ''; version++; historyVersion++; $('history').close(); $('workspace').hidden = true; $('login').hidden = false; $('logout').hidden = true; $('login-form').reset(); $('targets').replaceChildren(); $('project-list').replaceChildren(); }
async function api(path, method = 'GET', body) {
 const response = await fetch('/api/v1' + path, {method, headers:{Authorization:'Bearer ' + token, ...(body ? {'Content-Type':'application/json'} : {})}, ...(body ? {body:JSON.stringify(body)} : {}), cache:'no-store'});
 const data = await response.json();
 if (!response.ok) { if (response.status === 401) disconnect(); throw new Error(data.error || `请求失败 (${response.status})`); }
 return data;
}
async function run(task, button) { if (button) button.disabled = true; message(); try { await task(); } catch (e) { message(e.message, true); } finally { if (button) button.disabled = false; } }
function action(text, task) { const button = el('button', text); button.type = 'button'; button.onclick = () => run(task, button); return button; }
function tab(name) { for (const id of ['overview','projects','settings']) $(id).hidden = id !== name; document.querySelectorAll('[data-tab]').forEach(b => b.classList.toggle('active', b.dataset.tab === name)); }
async function connect(value) {
 token = value;
 await loadProjects();
 $('workspace').hidden = false; $('login').hidden = true; $('logout').hidden = false; $('login-form').reset(); tab('overview');
 await loadStatus();
}
async function loadProjects() {
 const data = await api('/projects'); projects = data.data;
 const selected = $('project-filter').value;
 $('project-filter').replaceChildren(new Option('全部项目',''));
 $('target-project').replaceChildren();
 for (const p of projects) { $('project-filter').add(new Option(p.name,p.id)); $('target-project').add(new Option(p.name,p.id)); }
 if (projects.some(p => String(p.id) === selected)) $('project-filter').value = selected;
 $('target-form').querySelector('button').disabled = !projects.length;
 $('project-list').replaceChildren();
 for (const p of projects) {
  const card = el('article',undefined,'card'); card.append(el('h3',p.name));
  const label = el('label','项目描述'); const input = el('textarea'); input.value = p.description; input.maxLength = 2000; label.append(input);
  card.append(label, action('保存描述',async()=> { checkBytes(input.value,2000,'描述'); await api(`/projects/${p.id}`,'PATCH',{description:input.value}); p.description = input.value; message('项目描述已保存'); }));
  $('project-list').append(card);
 }
 if (!projects.length) $('project-list').append(el('p','暂无项目，先创建一个项目。'));
}
function checkBytes(value, max, label) { if (new TextEncoder().encode(value).length > max) throw new Error(`${label}不能超过 ${max} UTF-8 字节`); }
async function loadStatus() {
 const current = ++version;
 const pid = $('project-filter').value; const params = new URLSearchParams({page,page_size:20});
 if ($('kind-filter').value) params.set('kind',$('kind-filter').value);
 const data = await api((pid ? `/projects/${pid}/status` : '/status') + '?' + params);
 if (current !== version || !token) return;
 pages = data.total_pages;
 $('counts').textContent = `共 ${data.total} 个检测目标 · 当前页 ${data.data.length} 个`;
 $('page-label').textContent = `${page} / ${Math.max(1,pages)}`;
 $('previous').disabled = page <= 1; $('next').disabled = page >= pages;
 $('targets').replaceChildren();
 for (const item of data.data) {
  const t = item.target, r = item.latest; const card = el('article',undefined,'card');
  card.append(el('span',states[item.state] || item.state,'badge ' + item.state),el('h3',t.name));
  const project = projects.find(p=>p.id===t.project_id);
  card.append(el('p',`${project?.name || '项目 ' + t.project_id} · ${t.kind === 'ssl' ? 'SSL 证书' : '服务可用性'}`),el('p',t.address));
  if (project?.description) card.append(el('p',project.description));
  card.append(el('p',`最近检测：${date(r?.checked_at)}\n延迟：${r ? r.latency_ms + ' ms' : '—'}`));
  if (r?.expires_at) card.append(el('p',`证书到期：${date(r.expires_at)}${item.expiry_warning ? ' · 即将到期或已过期' : ''}`,item.expiry_warning ? 'warning' : ''));
  if (r?.error) card.append(el('p',r.error,'warning'));
  const buttons = el('div',undefined,'actions');
  buttons.append(action('历史与统计',()=>openHistory(t)),action(t.enabled ? '暂停' : '恢复',async()=> { await api(`/targets/${t.id}`,'PATCH',{enabled:!t.enabled}); await loadStatus(); })); card.append(buttons); $('targets').append(card);
 }
 if (!data.data.length) $('targets').append(el('p','暂无检测目标，可在项目管理中添加。'));
}
async function loadSettings() { const s = await api('/settings'); $('smtp-status').textContent = `SMTP ${s.smtp_configured ? '已配置' : '未配置'} · 证书提前 ${s.ssl_warning_days} 天预警`; $('settings-form').elements.auto_notify.checked = s.auto_notify; $('settings-form').elements.auto_notify.disabled = !s.smtp_configured && !s.auto_notify; }
async function openHistory(t) { historyTarget = t; $('history-title').textContent = t.name + ' · 检测历史'; if (!$('history').open) $('history').showModal(); await resetHistory(); }
async function resetHistory() {
 const current = ++historyVersion;
 cursor = null; $('history-rows').replaceChildren(); $('stats').textContent = '加载中…'; $('more-history').hidden = true;
 const to = Math.floor(Date.now()/1000)+1; historyWindow = new URLSearchParams({from:to-Number($('history-range').value),to});
 const stats = await api(`/targets/${historyTarget.id}/stats?${historyWindow}`);
 if (current !== historyVersion || !$('history').open) return;
 $('stats').textContent = `${historyTarget.kind === 'ssl' ? 'TLS 校验成功率' : '样本可用率'}：${stats.availability_percent == null ? '—' : stats.availability_percent.toFixed(2)+'%'} · 样本 ${stats.samples} · 平均延迟 ${stats.avg_latency_ms == null ? '—' : stats.avg_latency_ms.toFixed(0)+' ms'}`;
 await loadHistory(current);
}
async function loadHistory(current = historyVersion) {
 const params = new URLSearchParams(historyWindow); params.set('limit',50); if (cursor) params.set('before_id',cursor);
 const data = await api(`/targets/${historyTarget.id}/history?${params}`);
 if (current !== historyVersion || !$('history').open) return;
 for (const r of data.data) { const row = el('tr'); for (const value of [date(r.checked_at),r.ok?'成功':'失败',r.latency_ms+' ms',r.http_status || '—',date(r.expires_at),r.error || '—']) row.append(el('td',String(value))); $('history-rows').append(row); }
 if (!$('history-rows').children.length) { const row = el('tr'); const cell = el('td','此时间范围暂无检测记录'); cell.colSpan = 6; row.append(cell); $('history-rows').append(row); }
 cursor = data.next_before_id; $('more-history').hidden = !cursor;
}
$('login-form').onsubmit = e => { e.preventDefault(); run(()=>connect(e.target.elements.token.value),e.submitter); };
$('logout').onclick = () => { disconnect(); message('已退出'); };
document.querySelectorAll('[data-tab]').forEach(button=>button.onclick=()=>run(async()=>{tab(button.dataset.tab); if(button.dataset.tab==='settings') await loadSettings();}));
$('refresh').onclick = () => run(loadStatus,$('refresh'));
for (const id of ['project-filter','kind-filter']) $(id).onchange=()=>run(async()=>{ page=1; await loadStatus(); });
$('previous').onclick = () => run(async()=>{page=Math.max(1,page-1); await loadStatus();});
$('next').onclick = () => run(async()=>{page++; await loadStatus();});
$('project-form').onsubmit=e=>{e.preventDefault();run(async()=>{ const form=e.target; const name=form.elements.name.value.trim(), description=form.elements.description.value; if(!name) throw new Error('请输入项目名称'); checkBytes(name,100,'名称'); checkBytes(description,2000,'描述'); await api('/projects','POST',{name,description}); form.reset(); await loadProjects(); message('项目已创建'); },e.submitter);};
const targetForm = $('target-form');
targetForm.elements.kind.onchange = () => { const ssl=targetForm.elements.kind.value==='ssl'; targetForm.elements.interval_seconds.value=ssl?86400:60; targetForm.elements.address.placeholder=ssl?'example.com（仅域名）':'https://example.com/health'; $('expected-label').hidden=ssl; };
targetForm.onsubmit=e=>{e.preventDefault();run(async()=>{const form=e.target, f=form.elements; const body={name:f.name.value.trim(),kind:f.kind.value,address:f.address.value.trim(),interval_seconds:Number(f.interval_seconds.value),timeout_seconds:Number(f.timeout_seconds.value),expected_status:Number(f.expected_status.value),enabled:true}; checkBytes(body.name,100,'名称'); if(body.timeout_seconds>=body.interval_seconds) throw new Error('超时必须小于检测间隔'); await api(`/projects/${f.project.value}/targets`,'POST',body); f.name.value=''; f.address.value=''; page=1; await loadStatus(); message('检测目标已添加');},e.submitter);};
$('settings-form').onsubmit=e=>{e.preventDefault();run(async()=>{await api('/settings','PATCH',{auto_notify:e.target.elements.auto_notify.checked}); await loadSettings(); message('通知设置已保存');},e.submitter);};
$('close-history').onclick=()=>{historyVersion++;$('history').close();};
$('history').addEventListener('close',()=>historyVersion++);
$('history-range').onchange=()=>run(resetHistory);
$('more-history').onclick=()=>run(()=>loadHistory(),$('more-history'));
if(tokens.length===1 && tokens[0]) run(()=>connect(tokens[0]));
