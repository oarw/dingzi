"use strict";
const workspace=document.getElementById('workspace');
const editor=document.getElementById('editor');
let routeVersion=0,MONITORS=[],RULES=[],CHANNELS=[],feedbackTimer,chartCleanup=()=>{};
const metricNames={cpu:'CPU',mem:'内存',swap:'交换',disk:'磁盘',load1:'负载',quota:'流量配额',offline:'机器离线',monitor:'服务故障'};
const statusNames={up:'正常',down:'故障',unknown:'未知',paused:'已停用',firing:'告警',recovered:'恢复',test:'测试',delivered:'已送达',failed:'发送失败',pending:'待发送',skipped:'已跳过'};
function icon(name){return '<span class="icon" aria-hidden="true" style="--icon:url(vendor/icons/'+name+'.svg)"></span>'}
function tool(name,label,action,id=''){return '<button type="button" class="icon-button" title="'+esc(label)+'" aria-label="'+esc(label)+'" data-action="'+action+'" data-id="'+id+'">'+icon(name)+'</button>'}
function command(label,action,id='',name='plus'){return '<button type="button" class="button" data-action="'+action+'" data-id="'+id+'">'+icon(name)+esc(label)+'</button>'}
function status(value){return '<span class="status '+esc(value)+'">'+esc(statusNames[value]||value)+'</span>'}
function stamp(at){return at?new Date(at*1000).toLocaleString('zh-CN',{hour12:false}):'暂无记录'}
function say(message){const box=document.getElementById('feedback');clearTimeout(feedbackTimer);box.textContent=message;box.hidden=false;feedbackTimer=setTimeout(()=>box.hidden=true,6000)}
async function api(path,method='GET',body){
  let response;
  try{response=await fetch('/api/v1/'+path,{method,headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(method==='POST'?75000:15000)})}
  catch{throw new Error('无法连接面板，请稍后重试')}
  const data=await response.json().catch(()=>({}));
  if(!response.ok){if(response.status===401){setAuth(false)}throw new Error(data.error||('请求失败 (HTTP '+response.status+')'))}
  return data;
}
function setAuth(value){AUTHED=value;document.getElementById('auth-button').textContent=value?'退出登录':'登录';render(FLEET.map(row=>adapt(row,Date.now()/1000)))}
function field(name,label,value='',options={}){
  const {type='text',full=false,min,max,step,required=true,placeholder='',choices}=options;
  const id='field-'+name;
  if(type==='checkbox')return '<label class="field check full" for="'+id+'"><input id="'+id+'" name="'+name+'" type="checkbox" '+(value?'checked':'')+'>'+esc(label)+'</label>';
  let input;
  if(choices)input='<select id="'+id+'" name="'+name+'" '+(required?'required':'')+'>'+choices.map(([v,l])=>'<option value="'+esc(v)+'" '+(String(v)===String(value)?'selected':'')+'>'+esc(l)+'</option>').join('')+'</select>';
  else input='<input id="'+id+'" name="'+name+'" type="'+type+'" value="'+esc(value)+'" '+(required?'required':'')+
    (min!==undefined?' min="'+min+'"':'')+(max!==undefined?' max="'+max+'"':'')+(step!==undefined?' step="'+step+'"':'')+
    ' '+(type==='text'?'maxlength="2048"':'')+' placeholder="'+esc(placeholder)+'" '+(type==='password'?'autocomplete="new-password"':'')+'>';
  return '<div class="field '+(full?'full':'')+'"><label for="'+id+'">'+esc(label)+'</label>'+input+'</div>';
}
function formDialog(title,contents,onSubmit,submitLabel='保存',extra){
  editor.innerHTML='<div class="editor-header"><h2 id="editor-title">'+esc(title)+'</h2>'+tool('x','关闭','close-editor')+'</div>'+
    '<form><div class="form-grid">'+contents+'</div><p class="form-error" role="alert" hidden></p><div class="form-actions">'+
    '<button type="button" class="button" data-action="close-editor">取消</button><button type="submit" class="button primary">'+icon('save')+esc(submitLabel)+'</button></div></form>';
  if(!editor.open)editor.showModal();
  const form=editor.querySelector('form');
  form.onsubmit=async e=>{
    e.preventDefault();const submit=form.querySelector('[type=submit]'),error=form.querySelector('.form-error');submit.disabled=true;error.hidden=true;
    try{await onSubmit(new FormData(form));editor.close()}catch(err){error.textContent=err.message;error.hidden=false}finally{submit.disabled=false}
  };
  if(extra)extra(form);
}
function showLogin(){
  formDialog('管理员登录',field('password','密码','',{type:'password',full:true}),async values=>{
    await api('login','POST',{password:values.get('password')});setAuth(true);await navigate();say('已登录');
  },'登录');
  editor.querySelector('input').autocomplete='current-password';
}
function confirmDelete(title,message,onSubmit){formDialog(title,'<p class="form-note">'+esc(message)+'</p>',onSubmit,'确认删除')}
function table(headers,rows){return '<div class="table-wrap"><table><thead><tr>'+headers.map(h=>'<th scope="col">'+esc(h)+'</th>').join('')+'</tr></thead><tbody>'+
  rows.map(row=>'<tr>'+row.map((cell,i)=>'<td data-label="'+esc(headers[i])+'"><div>'+cell+'</div></td>').join('')+'</tr>').join('')+'</tbody></table></div>'}
function heading(title,actions='',back=false){return '<div class="section-head">'+(back?'<a class="icon-button" href="#fleet" title="返回机器" aria-label="返回机器">'+icon('arrow-left')+'</a>':'')+'<h1>'+esc(title)+'</h1><div class="actions">'+actions+'</div></div>'}
function empty(message,action=''){return '<div class="empty"><p>'+esc(message)+'</p>'+action+'</div>'}
function options(items){return [['','请选择'],...items.map(x=>[x.id,x.name]) ]}
async function refreshAfter(message){await load();await navigate();say(message)}
async function navigate(){
  const version=++routeVersion;
  chartCleanup();chartCleanup=()=>{};
  const [view,id]=(location.hash.slice(1)||'fleet').split('/');
  const fleet=view==='fleet';
  document.getElementById('fleet-view').hidden=!fleet;workspace.hidden=fleet;
  document.querySelectorAll('[data-view]').forEach(a=>{if(a.dataset.view===view||(view==='machine'&&a.dataset.view==='fleet')||(view==='monitor'&&a.dataset.view==='monitors'))a.setAttribute('aria-current','page');else a.removeAttribute('aria-current')});
  if(fleet)return;
  if(!AUTHED&&!['machine'].includes(view)){workspace.innerHTML=heading('管理面板')+empty('请登录后管理监控和通知',command('登录','login','','log-in'));return}
  workspace.innerHTML='<div class="loading" role="status" aria-label="正在加载"></div>';
  try{
    if(view==='machine'){await machineDetail(Number(id),version);return}
    if(view==='monitor'){await monitorDetail(Number(id),version);return}
    if(view==='monitors'){
      const d=await api('monitors');if(version!==routeVersion)return;MONITORS=d.monitors;
      workspace.innerHTML=heading('服务监控',command('新建监控','monitor-new'))+
        (MONITORS.length?table(['名称 / 目标','执行探针','状态','延迟 / 丢包','24 小时可用率','最近检查','操作'],MONITORS.map(m=>[
          '<a href="#monitor/'+m.id+'">'+esc(m.name)+'</a><small>'+esc(m.type.toUpperCase()+' '+m.target)+'</small>',
          esc(FLEET.find(x=>x.id===m.server_id)?.name||'机器已删除'),status(!m.enabled?'paused':m.last?.status||'unknown')+(m.last?.error?'<small>'+esc(m.last.error)+'</small>':''),
          m.last&&m.last.status!=='unknown'?m.last.latency_ms.toFixed(2)+' ms / '+m.last.loss+'%':'--',m.uptime==null?'--':m.uptime.toFixed(2)+'%',stamp(m.last?.at),
          '<div class="actions">'+tool('play','立即检查 '+m.name,'monitor-run',m.id)+tool('settings','编辑 '+m.name,'monitor-edit',m.id)+tool('trash-2','删除 '+m.name,'monitor-delete',m.id)+'</div>'
        ])):empty('暂无服务监控',command('新建监控','monitor-new')));
    }else if(view==='channels'){
      const d=await api('channels');if(version!==routeVersion)return;CHANNELS=d.channels;
      workspace.innerHTML=heading('通知渠道',command('新建渠道','channel-new'))+(CHANNELS.length?table(['名称','类型','地址 / 会话','状态','操作'],CHANNELS.map(c=>[
        esc(c.name),c.type==='telegram'?'Telegram':'Webhook',esc(c.type==='telegram'?c.chat_id:c.url),status(c.enabled?'up':'paused'),
        '<div class="actions">'+tool('play','发送测试通知 '+c.name,'channel-test',c.id)+tool('settings','编辑 '+c.name,'channel-edit',c.id)+tool('trash-2','删除 '+c.name,'channel-delete',c.id)+'</div>'
      ])):empty('暂无通知渠道',command('新建渠道','channel-new')));
    }else if(view==='alerts'){
      const [d,c,m]=await Promise.all([api('alert-rules'),api('channels'),api('monitors')]);if(version!==routeVersion)return;
      RULES=d.rules;CHANNELS=c.channels;MONITORS=m.monitors;
      workspace.innerHTML=heading('告警规则',command('新建规则','rule-new'))+(RULES.length?table(['名称','对象','条件','持续时间','通知渠道','状态','操作'],RULES.map(a=>[
        esc(a.name),esc(a.metric==='monitor'?MONITORS.find(m=>m.id===a.monitor_id)?.name||'--':FLEET.find(m=>m.id===a.server_id)?.name||'--'),
        esc(metricNames[a.metric])+(a.metric==='offline'||a.metric==='monitor'?'':' >= '+a.threshold+(a.metric==='load1'?'':'%')),
        a.duration_seconds+' 秒',esc(CHANNELS.find(c=>c.id===a.channel_id)?.name||'--'),status(!a.enabled?'paused':a.active?'firing':a.since?'pending':'up'),
        '<div class="actions">'+tool('settings','编辑 '+a.name,'rule-edit',a.id)+tool('trash-2','删除 '+a.name,'rule-delete',a.id)+'</div>'
      ])):empty('暂无告警规则',command('新建规则','rule-new')));
    }else if(view==='events'){
      const d=await api('alert-events');if(version!==routeVersion)return;
      workspace.innerHTML=heading('事件记录',tool('refresh-cw','刷新事件','refresh'))+(d.events.length?table(['时间','事件','内容','通知状态','尝试次数'],d.events.map(e=>[
        stamp(e.at),status(e.state),esc(e.message),status(e.delivery)+(e.delivery_error?'<small>'+esc(e.delivery_error)+'</small>':''),String(e.attempts)
      ])):empty('暂无告警或通知记录'));
    }else{workspace.innerHTML=heading('页面不存在')+'<a href="#fleet">返回机器</a>'}
  }catch(err){if(version===routeVersion)workspace.innerHTML=heading('加载失败')+empty(err.message,command('重试','refresh','','refresh-cw'))}
}

async function machineDetail(id,version){
  let machine=FLEET.find(m=>m.id===id);
  if(!machine){await load();machine=FLEET.find(m=>m.id===id)}
  if(version!==routeVersion)return;
  if(!machine){workspace.innerHTML=heading('机器不存在','',true);return}
  workspace.innerHTML=heading(machine.name,(AUTHED?tool('settings','机器设置','machine-edit',id):command('登录','login','','log-in')),true)+
    '<dl class="detail-facts">'+[['系统',machine.platform+' / '+machine.arch],['处理器',machine.cpu_model||'--'],['内存',human(machine.mem_used)+' / '+human(machine.mem_total)],['磁盘',human(machine.disk_used)+' / '+human(machine.disk_total)],
    ['状态',machine.online?'在线':'离线'],['最近在线',stamp(machine.last_seen)],['本期流量',human(machine.traffic_in+machine.traffic_out)],['下次归零日',machine.reset_day+' 日 (UTC)']].map(([k,v])=>'<div><dt>'+esc(k)+'</dt><dd>'+esc(v)+'</dd></div>').join('')+'</dl>'+
    '<div class="chart-toolbar"><label>指标 <select id="chart-metric"><option value="resources">CPU / 内存 / 交换 / 磁盘</option><option value="network">网络速率</option><option value="load">系统负载</option></select></label><div class="segmented" aria-label="历史时间范围">'+[[1,'1 小时'],[6,'6 小时'],[24,'24 小时'],[168,'7 天'],[720,'30 天']].map(([n,label])=>'<button type="button" data-hours="'+n+'" aria-pressed="'+(n===1)+'">'+label+'</button>').join('')+'</div></div>'+
    '<div class="chart-key" id="chart-key"></div><div class="chart-area"><canvas id="history-chart" role="img" aria-label="机器历史指标" tabindex="0"></canvas></div><p id="chart-readout" class="chart-readout" aria-live="polite"></p><p id="chart-state" class="subtitle" role="status"></p>';
  let hours=1,points=[],request=0;
  const canvas=document.getElementById('history-chart');
  const graph=createGraph(canvas,()=>points,()=>({hours,metric:document.getElementById('chart-metric')?.value||'resources'}));chartCleanup=graph.cleanup;
  const getHistory=async()=>{
    const seq=++request;
    document.getElementById('chart-state').textContent='正在加载历史数据';
    try{const d=await api('servers/'+id+'/history?hours='+hours+'&buckets=200');if(version!==routeVersion||seq!==request)return;points=d.points||[];graph.draw();document.getElementById('chart-state').textContent=points.length?'共 '+points.length+' 个聚合采样点':'所选时间范围暂无数据'}
    catch(err){if(version===routeVersion&&seq===request){points=[];graph.draw();document.getElementById('chart-state').textContent=err.message}}
  };
  workspace.querySelectorAll('[data-hours]').forEach(b=>b.onclick=()=>{hours=Number(b.dataset.hours);workspace.querySelectorAll('[data-hours]').forEach(x=>x.setAttribute('aria-pressed',String(x===b)));getHistory()});
  document.getElementById('chart-metric').onchange=graph.draw;
  await getHistory();
}

function createGraph(canvas,readPoints,readOptions){
  let geometry,selected=-1;
  function series(){const metric=readOptions().metric;return metric==='network'?[['net_in_speed','入站','#258dc2'],['net_out_speed','出站','#bf568d']]:metric==='load'?[['load1','1 分钟负载','#258dc2']]:[['cpu','CPU','#258dc2'],['mem','内存','#ae7800'],['swap','交换','#b95683'],['disk','磁盘','#218758']]}
  function draw(){
    if(!canvas.isConnected)return;
    const points=readPoints(),opts=readOptions(),lines=series(),ctx=canvas.getContext('2d'),ratio=devicePixelRatio||1;
    const width=canvas.clientWidth,height=canvas.clientHeight;
    if(!width)return;
    canvas.width=Math.round(width*ratio);canvas.height=Math.round(height*ratio);ctx.scale(ratio,ratio);
    const style=getComputedStyle(document.documentElement),ink=style.getPropertyValue('--ink-dim').trim(),edge=style.getPropertyValue('--edge').trim();
    const left=60,right=16,top=18,bottom=34,w=width-left-right,h=height-top-bottom;
    const end=Date.now()/1000,start=end-opts.hours*3600;
    const values=points.flatMap(p=>lines.map(([key])=>p[key]).filter(v=>typeof v==='number'&&Number.isFinite(v)));
    const max=opts.metric==='resources'?100:Math.max(1,...values)*1.12;
    geometry={left,top,w,h,start,end,max,lines};
    ctx.font='11px system-ui';ctx.lineWidth=1;
    for(let i=0;i<=4;i++){const y=top+h-i*h/4;ctx.strokeStyle=edge;ctx.beginPath();ctx.moveTo(left,y);ctx.lineTo(left+w,y);ctx.stroke();ctx.fillStyle=ink;ctx.textAlign='right';const v=max*i/4;ctx.fillText(opts.metric==='network'?human(v):Number(v.toFixed(1))+(opts.metric==='resources'?'%':''),left-8,y+4)}
    for(let i=0;i<=3;i++){ctx.textAlign=i===0?'left':i===3?'right':'center';const at=start+(end-start)*i/3;const date=new Date(at*1000);ctx.fillStyle=ink;ctx.fillText(opts.hours>24?date.toLocaleDateString('zh-CN',{month:'numeric',day:'numeric'}):date.toLocaleTimeString('zh-CN',{hour:'2-digit',minute:'2-digit',hour12:false}),left+w*i/3,height-9)}
    for(const [key,label,color] of lines){ctx.strokeStyle=color;ctx.fillStyle=color;ctx.lineWidth=1.8;ctx.beginPath();let previous=null;
      for(const p of points){const value=p[key];if(value==null||!Number.isFinite(value)){previous=null;continue}const x=left+(p.at-start)/(end-start)*w,y=top+h-Math.min(max,Math.max(0,value))/max*h;
        if(previous==null||p.at-previous>(end-start)/200*2.5)ctx.moveTo(x,y);else ctx.lineTo(x,y);previous=p.at;
      }ctx.stroke();if(points.length===1&&points[0][key]!=null){const p=points[0];ctx.beginPath();ctx.arc(left+(p.at-start)/(end-start)*w,top+h-p[key]/max*h,3,0,Math.PI*2);ctx.fill()}
    }
    const key=document.getElementById('chart-key');if(key)key.innerHTML=lines.map(([,label,color])=>'<span><i style="--line:'+color+'"></i>'+label+'</span>').join('');
    if(selected>=0&&points[selected]){const p=points[selected],x=left+(p.at-start)/(end-start)*w;ctx.strokeStyle=ink;ctx.setLineDash([3,4]);ctx.beginPath();ctx.moveTo(x,top);ctx.lineTo(x,top+h);ctx.stroke();ctx.setLineDash([])}
  }
  function select(index){const points=readPoints();if(!points.length)return;selected=Math.max(0,Math.min(points.length-1,index));const p=points[selected];document.getElementById('chart-readout').textContent=stamp(p.at)+'  '+series().map(([key,label])=>label+' '+(p[key]==null?'--':readOptions().metric==='network'?human(p[key])+'/s':Number(p[key].toFixed(2))+(readOptions().metric==='resources'?'%':''))).join(' / ');draw()}
  canvas.onpointermove=e=>{if(!geometry)return;const at=geometry.start+(e.clientX-canvas.getBoundingClientRect().left-geometry.left)/geometry.w*(geometry.end-geometry.start);const points=readPoints();let nearest=0;for(let i=1;i<points.length;i++)if(Math.abs(points[i].at-at)<Math.abs(points[nearest].at-at))nearest=i;select(nearest)};
  canvas.onkeydown=e=>{if(['ArrowLeft','ArrowRight'].includes(e.key)){e.preventDefault();select(selected+(e.key==='ArrowLeft'?-1:1))}};
  const observer=new ResizeObserver(draw);observer.observe(canvas);
  return {draw,cleanup:()=>observer.disconnect()};
}

async function monitorDetail(id,version){
  const [d,h]=await Promise.all([api('monitors'),api('monitors/'+id+'/history')]);if(version!==routeVersion)return;MONITORS=d.monitors;
  const m=MONITORS.find(x=>x.id===id);if(!m){workspace.innerHTML=heading('监控不存在');return}
  workspace.innerHTML=heading(m.name,tool('play','立即检查','monitor-run',id)+tool('settings','编辑监控','monitor-edit',id))+'<a class="text-link" href="#monitors">'+icon('arrow-left')+'返回服务监控</a>'+
    '<p class="subtitle">'+esc(m.type.toUpperCase()+' '+m.target)+' · '+m.interval_seconds+' 秒 / 次 · 24 小时可用率 '+(m.uptime==null?'--':m.uptime.toFixed(2)+'%')+'</p>'+
    (h.points.length?table(['时间','状态','延迟','丢包','HTTP 状态','结果'],h.points.map(p=>[stamp(p.at),status(p.status),p.status==='unknown'?'--':p.latency_ms+' ms',p.status==='unknown'?'--':p.loss+'%',p.status_code||'--',esc(p.error)||'正常'])):empty('暂无检查记录'));
}

function editMachine(id){
  const m=FLEET.find(x=>x.id===id);if(!m){say('机器不存在');return}
  formDialog('机器设置',field('name','名称',m.name,{full:true})+field('quota','月度配额 (GiB，0 为不限)',m.quota/1073741824,{type:'number',min:0,max:8388607,step:'any',full:true})+
    field('reset_day','归零日 (UTC)',m.reset_day,{type:'number',min:1,max:31})+field('count_mode','计费口径',m.quota_mode,{choices:[['sum','入站 + 出站'],['out','仅出站'],['max','入站、出站取较大值']]})+
    '<p class="form-note">修改归零日会清空当前周期的累计流量。每月不足该日期时在月末归零。</p>',async f=>{
      await api('servers/'+id,'PATCH',{name:f.get('name'),quota:Math.round(Number(f.get('quota'))*1073741824),reset_day:Number(f.get('reset_day')),count_mode:f.get('count_mode')});await refreshAfter('机器设置已保存');
    });
  const del=document.createElement('button');del.type='button';del.className='button danger';del.textContent='删除机器';del.onclick=()=>confirmDelete('删除 '+m.name,'将删除机器及其历史、关联监控和规则，并吊销该身份。此操作无法撤销。',async()=>{await api('servers/'+id,'DELETE');location.hash='fleet';await refreshAfter('机器已删除')});editor.querySelector('.form-actions').prepend(del);
}
function editMonitor(id){
  const m=MONITORS.find(x=>x.id===id)||{name:'',server_id:'',type:'http',target:'',interval_seconds:60,timeout_ms:5000,enabled:true};
  if(!FLEET.length){say('请先接入探针');return}
  formDialog(id?'编辑服务监控':'新建服务监控',field('name','名称',m.name,{full:true})+field('server_id','执行探针',m.server_id,{choices:options(FLEET),full:true})+
    field('type','类型',m.type,{choices:[['http','HTTP / HTTPS'],['tcp','TCP'],['ping','ICMP Ping']]})+field('interval_seconds','检查间隔 (秒)',m.interval_seconds,{type:'number',min:10,max:86400})+
    field('target','目标',m.target,{full:true,placeholder:'https://example.com/health'})+field('timeout_ms','超时 (毫秒)',m.timeout_ms,{type:'number',min:100,max:60000})+field('enabled','启用监控',m.enabled,{type:'checkbox'}),async f=>{
      await api('monitors'+(id?'/'+id:''),id?'PUT':'POST',{name:f.get('name'),server_id:Number(f.get('server_id')),type:f.get('type'),target:f.get('target'),interval_seconds:Number(f.get('interval_seconds')),timeout_ms:Number(f.get('timeout_ms')),enabled:f.has('enabled')});await refreshAfter('监控已保存');
    },'保存',form=>{const type=form.elements.type,target=form.elements.target;const change=()=>{target.placeholder=type.value==='http'?'https://example.com/health':type.value==='tcp'?'example.com:443':'example.com'};type.onchange=change;change()});
}
function editChannel(id){
  const c=CHANNELS.find(x=>x.id===id)||{name:'',type:'webhook',url:'',chat_id:'',enabled:true};
  formDialog(id?'编辑通知渠道':'新建通知渠道',field('name','名称',c.name,{full:true})+field('type','类型',c.type,{choices:[['webhook','Webhook'],['telegram','Telegram']],full:true})+
    field('url','Webhook 地址',c.url,{full:true})+field('token',c.token_set?'密钥 / Bot Token（留空保留）':'密钥 / Bot Token','',{type:'password',required:false,full:true})+
    field('chat_id','Chat ID',c.chat_id,{full:true,required:false})+field('clear_token','清除 Webhook 密钥',false,{type:'checkbox'})+field('enabled','启用渠道',c.enabled,{type:'checkbox'}),async f=>{
      await api('channels'+(id?'/'+id:''),id?'PUT':'POST',{name:f.get('name'),type:f.get('type'),url:f.get('url'),token:f.get('token'),chat_id:f.get('chat_id'),clear_token:f.has('clear_token'),enabled:f.has('enabled')});await refreshAfter('渠道已保存');
    },'保存',form=>{const update=()=>{const tg=form.elements.type.value==='telegram';for(const name of ['url','chat_id','clear_token'])form.elements[name].closest('.field').hidden=name==='url'||name==='clear_token'?tg:!tg;form.elements.url.required=!tg;form.elements.chat_id.required=tg;form.elements.token.required=tg&&!c.token_set};form.elements.type.onchange=update;update()});
}
async function editRule(id){
  const [channels,monitors,rules]=await Promise.all([api('channels'),api('monitors'),api('alert-rules')]);CHANNELS=channels.channels;MONITORS=monitors.monitors;RULES=rules.rules;
  if(!CHANNELS.length){say('请先创建通知渠道');location.hash='channels';return}
  const a=RULES.find(x=>x.id===id)||{name:'',metric:'offline',server_id:'',monitor_id:'',threshold:90,duration_seconds:60,channel_id:CHANNELS[0].id,enabled:true};
  formDialog(id?'编辑告警规则':'新建告警规则',field('name','名称',a.name,{full:true})+field('metric','指标',a.metric,{choices:Object.entries(metricNames),full:true})+
    field('server_id','机器',a.server_id,{choices:options(FLEET),full:true})+field('monitor_id','服务监控',a.monitor_id,{choices:options(MONITORS),full:true})+
    field('threshold','阈值（大于或等于）',a.threshold,{type:'number',min:0,max:1000000,step:'any'})+field('duration_seconds','持续时间 (秒)',a.duration_seconds,{type:'number',min:0,max:86400})+
    field('channel_id','通知渠道',a.channel_id,{choices:options(CHANNELS),full:true})+field('enabled','启用规则',a.enabled,{type:'checkbox'}),async f=>{
      await api('alert-rules'+(id?'/'+id:''),id?'PUT':'POST',{name:f.get('name'),metric:f.get('metric'),server_id:Number(f.get('server_id')),monitor_id:Number(f.get('monitor_id')),threshold:Number(f.get('threshold')),duration_seconds:Number(f.get('duration_seconds')),channel_id:Number(f.get('channel_id')),enabled:f.has('enabled')});await refreshAfter('规则已保存');
    },'保存',form=>{const update=()=>{const metric=form.elements.metric.value,isMonitor=metric==='monitor';form.elements.server_id.closest('.field').hidden=isMonitor;form.elements.server_id.required=!isMonitor;form.elements.monitor_id.closest('.field').hidden=!isMonitor;form.elements.monitor_id.required=isMonitor;form.elements.threshold.closest('.field').hidden=isMonitor||metric==='offline';form.elements.threshold.disabled=isMonitor||metric==='offline';form.elements.threshold.max=metric==='quota'?1000:metric==='load1'?1000000:100};form.elements.metric.onchange=update;update()});
}

document.addEventListener('click',async e=>{
  const b=e.target.closest('[data-action]');if(!b)return;
  const action=b.dataset.action,id=Number(b.dataset.id);
  if(action==='close-editor'){editor.close();return}
  if(action==='login'){showLogin();return}
  if(action==='refresh'){await navigate();return}
  if(!AUTHED){showLogin();return}
  try{
    if(action==='machine-edit')editMachine(id);
    else if(action==='monitor-new'||action==='monitor-edit')editMonitor(id);
    else if(action==='channel-new'||action==='channel-edit')editChannel(id);
    else if(action==='rule-new'||action==='rule-edit')await editRule(id);
    else if(action==='monitor-run'){b.disabled=true;await api('monitors/'+id+'/run','POST',{});await navigate();say('检查完成')}
    else if(action==='channel-test'){b.disabled=true;await api('channels/'+id+'/test','POST',{});say('测试通知已送达')}
    else if(action.endsWith('-delete')){
      const kind=action.split('-')[0],source=kind==='monitor'?MONITORS:kind==='channel'?CHANNELS:RULES,item=source.find(x=>x.id===id),path=kind==='monitor'?'monitors':kind==='channel'?'channels':'alert-rules';
      confirmDelete('删除 '+(item?.name||''),kind==='monitor'?'将删除检查历史及关联的告警规则。此操作无法撤销。':'此操作无法撤销。',async()=>{await api(path+'/'+id,'DELETE');await refreshAfter('已删除')});
    }
  }catch(err){say(err.message)}finally{b.disabled=false}
});
document.getElementById('auth-button').onclick=async()=>{if(!AUTHED){showLogin();return}try{await api('logout','POST',{});closeTerm();setAuth(false);await navigate();say('已退出登录')}catch(err){say(err.message)}};
window.addEventListener('hashchange',navigate);
window.addEventListener('online',load);
document.querySelectorAll('[data-t]').forEach(b=>b.addEventListener('click',()=>{if(location.hash.startsWith('#machine/'))navigate()}));
(async()=>{await load();try{setAuth((await api('session')).authed)}catch{}await navigate()})();
