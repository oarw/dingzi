"use strict";
const T={caution:70,warn:90};
const band=v=>v>=T.warn?"warn":v>=T.caution?"caution":"";
const qband=v=>v>=95?"warn":v>=80?"caution":"";
// esc guards every agent-supplied string on its way into HTML. The machine name
// comes from a hostname or an operator's input, and the platform strings come
// from the agent, so none of them are the panel's to trust.
function esc(s){return String(s==null?"":s)
  .replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;")
  .replace(/"/g,"&quot;").replace(/'/g,"&#39;")}
function human(b){const u=["B","K","M","G","T","P"];let i=0;
  while(b>=1024&&i<u.length-1){b/=1024;i++}
  return (b<10&&i>0?b.toFixed(1):Math.round(b))+u[i]}
function dur(s){if(!s)return "—";const d=Math.floor(s/86400);
  if(d>0)return d+"天";const h=Math.floor(s/3600);
  if(h>0)return h+"小时";return Math.max(1,Math.floor(s/60))+"分钟"}
function segs(v,cls){let h='<div class="segs" aria-hidden="true">';
  const on=Math.round(v/5);
  for(let i=0;i<20;i++)h+='<div class="seg'+(i<on?" on "+cls:"")+'"></div>';
  return h+"</div>"}
// Tier 1: loud. Lit-segment count carries magnitude; the text marker carries
// caution and warn without relying on hue.
function sat(k,v,off){
  if(off)return '<div class="mod"><div class="mod-k">'+k+'</div><div class="well">'+
    '<span class="num dead">——</span></div>'+segs(0,"")+'</div>';
  v=Math.round(v*10)/10;
  const c=band(v), tone=v===0?"zero":c;
  return '<div class="mod"><div class="mod-k">'+k+'</div><div class="well">'+
    '<span class="num '+tone+'">'+v+'%</span>'+
    (c?'<span class="sub">'+(c==="warn"?"告警":"注意")+'</span>':'')+
    '</div>'+segs(v,c)+'</div>'}
function billedTraffic(inBytes,outBytes,mode){
  return mode==='out'?outBytes:mode==='max'?Math.max(inBytes,outBytes):inBytes+outBytes;
}
function quotaState(m){
  if(!(m.quota_bytes>0))return {percent:0,tone:'',label:'',excess:0};
  const used=billedTraffic(m.net_in_total,m.net_out_total,m.quota_mode||'sum');
  const percent=used/m.quota_bytes*100,tone=qband(percent);
  const label=percent>100?'配额超额':percent===100?'配额已用尽':percent>=95?'配额将耗尽':tone?'临近配额':'';
  return {percent,tone,label,excess:Math.max(0,used-m.quota_bytes)};
}
function resourceWarning(m){return m.online&&[m.cpu,m.mem,m.swap,m.disk].some(v=>v>=T.warn)}
function quotaWarning(m){return m.online&&!!quotaState(m).tone}
function quota(m){
  const io='<span class="fv"><i>↓</i>'+human(m.net_in_total)+
    ' <i>↑</i>'+human(m.net_out_total)+'</span>';
  if(!m.quota_bytes)return '<div class="fx w2"><span class="fk">流量</span>'+io+
    '<span class="fv k">未设配额</span></div>';
  const mode=m.quota_mode||"sum";
  const q=quotaState(m),p=Math.round(q.percent*10)/10;
  const modeName={sum:'进出合计',out:'仅出站',max:'进出取较大'}[mode]||'进出合计';
  return '<div class="fx w2"><span class="fk">流量</span>'+io+
    '<span class="fv quota-value '+q.tone+'">'+p+'% / '+human(m.quota_bytes)+
    ' · '+modeName+'</span></div>'+
    (q.label?'<p class="w2 quota-note '+q.tone+'">'+q.label+(q.excess?' · 超出 '+human(q.excess):'')+'</p>':'')+
    '<div class="w2 quota">'+segs(Math.min(100,q.percent),q.tone)+'<span class="limit"></span></div>'}
function card(m){
  const off=!m.online;
  const hot=resourceWarning(m),q=quotaState(m);
  return '<article class="mc'+(off?" is-off":"")+(!off&&(hot||q.tone==='warn')?" is-warn":"")+'">'+
    '<div class="mc-head">'+
      '<span class="lamp'+(off?" off":"")+'" aria-hidden="true"></span>'+
      '<a class="mc-name" href="#machine/'+m.id+'">'+esc(m.name)+'</a>'+
      (off?'<span class="mc-flag off">离线</span>':
       hot?'<span class="mc-flag">告警</span>':'')+
      (!off&&q.label?'<span class="mc-flag quota-flag '+q.tone+'">'+q.label+'</span>':'')+
      '<span class="mc-spec">'+esc(m.platform)+' '+esc(m.platform_version)+'<br>'+
        esc(m.arch)+' · '+m.cores+'核'+m.threads+'线</span>'+
      // Shown only where a terminal can actually open: the panel allows it, the
      // agent allows it, and the machine is online.
      ((AUTHED&&PANEL_TERMINAL&&m.terminal&&!off)
        ?'<button class="mc-term" data-id="'+m.id+'" data-name="'+esc(m.name)+
         '" title="打开终端" aria-label="打开 '+esc(m.name)+' 的终端">'+icon('terminal')+'</button>':'')+
    '</div>'+
    '<div class="sat">'+sat("CPU",m.cpu,off)+sat("内存",m.mem,off)+
      sat("交换",m.swap,off)+sat("磁盘",m.disk,off)+'</div>'+
    '<div class="facts">'+
      '<div class="fx"><span class="fk">负载</span><span class="fv'+(off?" k":"")+'">'+
        (off?"——":m.load.map(x=>x.toFixed(2)).join('<em>/</em>'))+'</span></div>'+
      '<div class="fx"><span class="fk">在线</span><span class="fv'+(off?" k":"")+'">'+
        (off?"离线 "+dur(m.offline_for):dur(m.uptime))+'</span></div>'+
      '<div class="fx w2"><span class="fk">网速</span><span class="fv'+(off?" k":"")+'">'+
        (off?"——":'<i>↓</i>'+human(m.net_in_speed)+'/s <i>↑</i>'+
          human(m.net_out_speed)+'/s')+'</span></div>'+
      (off?'<div class="fx w2"><span class="fk">流量</span>'+
        '<span class="fv k">——</span></div>':quota(m))+
    '</div><div class="machine-actions"><a class="text-link" href="#machine/'+m.id+'">'+icon('chart-no-axes-combined')+'历史</a>'+
      (AUTHED?tool('settings','设置 '+m.name,'machine-edit',m.id):'')+'</div></article>'}

const grid=document.getElementById("grid");
let FLEET_FILTER='all';
const fleetFilters={online:{label:'在线',match:m=>m.online},warn:{label:'指标异常',match:resourceWarning},quota:{label:'配额提醒',match:quotaWarning},offline:{label:'离线',match:m=>!m.online}};
function setFleetFilter(filter){
  FLEET_FILTER=filter in fleetFilters?filter:'all';
  location.hash='fleet';
  render(FLEET.map(row=>adapt(row,Date.now()/1000)));
}
document.querySelectorAll('[data-fleet-filter]').forEach(button=>button.addEventListener('click',()=>setFleetFilter(FLEET_FILTER===button.dataset.fleetFilter?'all':button.dataset.fleetFilter)));
document.getElementById('clear-fleet-filter').addEventListener('click',()=>setFleetFilter('all'));
function render(list){
  const focused=document.activeElement;
  const key=grid.contains(focused)?{href:focused.getAttribute('href'),id:focused.dataset.id,action:focused.dataset.action}:null;
  const filter=fleetFilters[FLEET_FILTER];
  const visible=filter?list.filter(filter.match):list;
  const markup=visible.map(card).join("");
  if(grid.innerHTML!==markup){
    grid.innerHTML=markup;
    if(key){const match=[...grid.querySelectorAll('a,button')].find(el=>key.href?el.getAttribute('href')===key.href:el.dataset.id===key.id&&el.dataset.action===key.action);if(match)match.focus({preventScroll:true})}
  }
  const on=list.filter(m=>m.online).length;
  const hot=list.filter(resourceWarning).length;
  document.getElementById("n-on").textContent=on;
  document.getElementById("n-warn").textContent=hot;
  document.getElementById("n-off").textContent=list.length-on;
  document.getElementById('n-quota').textContent=list.filter(quotaWarning).length;
  document.querySelectorAll('[data-fleet-filter]').forEach(button=>button.setAttribute('aria-pressed',String(button.dataset.fleetFilter===FLEET_FILTER)));
  document.getElementById('clear-fleet-filter').hidden=!filter;
  const status=document.getElementById('fleet-filter-status');
  status.hidden=!filter;
  const message=filter?filter.label+' · '+visible.length+' 台 / 共 '+list.length+' 台':'';
  if(status.textContent!==message)status.textContent=message;
  document.getElementById('fleet-empty').hidden=!FLEET_READY||list.length>0;
  document.getElementById('fleet-filter-empty').hidden=!FLEET_READY||!list.length||visible.length>0;
}
// PANEL_TERMINAL is whether the panel itself allows terminals. A machine also
// has to have an agent started with --allow-terminal; both must agree before the
// button appears, so an operator is never offered a control that always fails.
let PANEL_TERMINAL=false,AUTHED=true,FLEET=[],loadingFleet=false,FLEET_READY=false;
let freshnessTimer;
function connectionState(state){
  clearTimeout(freshnessTimer);
  document.getElementById('data-indicator').dataset.state=state;
  const status=document.getElementById('data-state'),label={live:'已连接',offline:'连接中断',stale:'待更新',loading:'连接中'}[state];
  if(status.textContent!==label)status.textContent=label;
  if(state==='live')freshnessTimer=setTimeout(()=>{connectionState('stale');grid.classList.add('stale')},15000);
}

// Keep the renderer independent of API field names.
function adapt(row,now){
  return {id:row.id,name:row.name,online:row.online,
    platform:row.platform,platform_version:"",arch:row.arch,
    cores:row.cpu_cores,threads:row.cpu_threads,
    cpu:row.cpu,mem:row.mem,swap:row.swap,disk:row.disk,
    load:[row.load1,row.load5,row.load15],
    uptime:row.uptime,
    offline_for:row.last_seen?Math.max(0,now-row.last_seen):0,
    net_in_speed:row.net_in_speed,net_out_speed:row.net_out_speed,
    net_in_total:row.traffic_in,net_out_total:row.traffic_out,
    quota_bytes:row.quota,quota_mode:row.quota_mode,
    terminal:row.terminal_enabled,skew_ms:row.skew_ms||0};
}
async function load(){
  if(loadingFleet)return;
  loadingFleet=true;
  try{
    const r=await fetch("/api/v1/servers",{headers:{"Accept":"application/json"},signal:AbortSignal.timeout(10000)});
    if(r.status===401){location.replace("/admin/login");return}
    if(!r.ok)throw new Error("HTTP "+r.status);
    const d=await r.json();
    PANEL_TERMINAL=!!d.terminal_enabled;
    FLEET=d.servers||[];
    FLEET_READY=true;
    render((d.servers||[]).map(row=>adapt(row,d.now)));
    // An empty but reachable fleet is a real state, and it is not the same as a
    // backend that is down. Saying so beats an unexplained empty page.
    document.getElementById('connection-status').hidden=true;
    grid.classList.remove('stale');
    connectionState('live');
    document.getElementById("src").textContent='机器数据更新 '+new Date(d.now*1000).toLocaleTimeString('zh-CN');
  }catch{
    PANEL_TERMINAL=false;
    FLEET_READY=false;
    connectionState('offline');
    // A failed request is not proof that no agents have been enrolled.
    document.getElementById('fleet-empty').hidden=true;
    document.getElementById('fleet-filter-empty').hidden=true;
    const status=document.getElementById('connection-status');
    status.hidden=false;status.textContent='面板连接中断，正在重试'+(FLEET.length?'；以下为最后一次收到的数据':'');
    grid.classList.add('stale');
  }finally{loadingFleet=false}
}
document.addEventListener('visibilitychange',()=>{if(!document.hidden)load()});

const Q=new URLSearchParams(location.search);
const STILL=Q.has("still");
function tick(){const d=new Date();document.getElementById("clk").textContent=
  [d.getHours(),d.getMinutes(),d.getSeconds()]
    .map(x=>String(x).padStart(2,"0")).join(":")}
if(STILL){document.getElementById("clk").textContent="14:32:07"}
else{tick();setInterval(tick,1000);
  // Poll. Without this the board shows whatever was true when the tab opened,
  // which for a monitoring panel is worse than showing nothing.
  setInterval(()=>{if(!document.hidden)load()},2000);}

/* ── terminal ─────────────────────────────────────────────────────────────── */
const TW=document.getElementById("term-wrap");
let TERM=null,FIT=null,WS=null,LASTFOCUS=null;

// Themes for xterm. Reusing the board's own tokens rather than xterm's defaults,
// so the terminal is the same object as the panel around it.
function termTheme(){
  const cs=getComputedStyle(document.documentElement);
  const v=n=>cs.getPropertyValue(n).trim();
  const light=document.documentElement.dataset.theme==="light"||
    (document.documentElement.dataset.theme==="auto"&&
     !window.matchMedia("(prefers-color-scheme: dark)").matches);
  return light
    ? {background:v("--well")||"#e2e2df",foreground:"#15171a",cursor:"#15171a",
       selectionBackground:"rgba(0,0,0,.18)"}
    : {background:v("--well")||"#080a0d",foreground:"#e9e7e2",cursor:v("--val"),
       selectionBackground:"rgba(255,176,0,.28)"};
}

function closeTerm(){
  TW.dataset.open="";
  TW.close();
  if(WS){WS.onmessage=null;WS.onerror=null;WS.onclose=null;WS.close();WS=null}
  if(TERM){TERM.dispose();TERM=null;FIT=null}
  document.getElementById("term-shell").textContent="";
  if(LASTFOCUS&&document.body.contains(LASTFOCUS)){LASTFOCUS.focus()}
}

function openTerm(id,name){
  if(!AUTHED){showLogin();return}
  if(TERM)closeTerm();
  LASTFOCUS=document.activeElement;
  document.getElementById("term-title").textContent="终端 · "+name;
  document.getElementById("term-shell").textContent="";
  TW.dataset.open="1";
  TW.showModal();

  TERM=new Terminal({
    fontFamily:getComputedStyle(document.documentElement)
      .getPropertyValue("--mono").trim()||"monospace",
    fontSize:13,lineHeight:1.2,cursorBlink:true,
    scrollback:2000,theme:termTheme(),
    // Terminal output is bytes, not text. Letting xterm decode UTF-8 itself from
    // the raw stream is what makes a Chinese filename arrive intact even when it
    // is split across two frames.
    convertEol:false});
  FIT=new FitAddon.FitAddon();
  TERM.loadAddon(FIT);
  TERM.open(document.getElementById("term-body"));
  FIT.fit();
  TERM.focus();

  const proto=location.protocol==="https:"?"wss:":"ws:";
  const url=proto+"//"+location.host+"/api/v1/servers/"+id+
    "/terminal?cols="+TERM.cols+"&rows="+TERM.rows;
  WS=new WebSocket(url);
  WS.binaryType="arraybuffer";
  const socket=WS,terminal=TERM;
  const active=()=>WS===socket&&TERM===terminal;

  WS.onmessage=ev=>{
    if(!active())return;
    if(typeof ev.data==="string"){
      // Text frames are control messages. Notices are printed into the pane so a
      // refusal reads as words instead of an empty terminal.
      let c;try{c=JSON.parse(ev.data)}catch{return}
      if(c.type==="notice"){terminal.writeln("\x1b[33m"+c.message+"\x1b[0m")}
      else if(c.type==="ready"){
        document.getElementById("term-shell").textContent=c.shell||"";
      }
      return;
    }
    terminal.write(new Uint8Array(ev.data));
  };
  WS.onerror=()=>{if(active())terminal.writeln("\x1b[31m连接失败\x1b[0m")};
  WS.onclose=()=>{if(active())terminal.writeln("\r\n\x1b[90m[连接已关闭]\x1b[0m")};

  // Keystrokes go out as binary so the bytes the shell receives are exactly the
  // bytes the terminal produced.
  const enc=new TextEncoder();
  TERM.onData(d=>{if(active()&&socket.readyState===1)socket.send(enc.encode(d))});

  // Resizing has to reach the pty, or top and vi draw into the wrong box.
  TERM.onResize(({cols,rows})=>{
    if(active()&&socket.readyState===1){
      socket.send(JSON.stringify({type:"resize",cols,rows}));
    }
  });
}

let fitTimer=null;
window.addEventListener("resize",()=>{
  if(!FIT)return;
  clearTimeout(fitTimer);
  fitTimer=setTimeout(()=>{try{FIT.fit()}catch{}},80);
});

document.getElementById("term-x").addEventListener("click",closeTerm);
TW.addEventListener('cancel',e=>{e.preventDefault();closeTerm()});
document.addEventListener("keydown",e=>{
  // Escape closes only when the terminal is open, and only when it is not the
  // shell that wants the key: xterm consumes Escape for the application, so this
  // listener sees it on the document only when the pane has no focus.
  if(e.key==="Escape"&&TW.dataset.open==="1"&&
     !document.getElementById("term-body").contains(document.activeElement)){
    closeTerm();
  }
});
// Delegated, because cards are re-rendered on every poll and a listener bound to
// a button would be discarded two seconds later.
grid.addEventListener("click",e=>{
  const b=e.target.closest(".mc-term");
  if(!b)return;
  openTerm(b.dataset.id,b.dataset.name);
});
