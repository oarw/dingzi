"use strict";
let publicLoading=false,publicHasData=false;
const node=(tag,cls,text)=>{const el=document.createElement(tag);el.className=cls;if(text!==undefined)el.textContent=text;return el};
async function refreshPublic(){
  if(publicLoading)return;
  publicLoading=true;
  const grid=document.getElementById('public-grid'),error=document.getElementById('public-error');
  try{
    const response=await fetch('/api/v1/public/servers',{signal:AbortSignal.timeout(10000)});
    if(!response.ok)throw new Error('unavailable');
    const data=await response.json(),rows=data.servers||[];
    document.title=data.site_name;
    document.getElementById('site-name').textContent=data.site_name;
    document.getElementById('site-description').textContent=data.description;
    document.getElementById('public-tally').textContent=rows.filter(m=>m.online).length+' 台在线 / 共 '+rows.length+' 台';
    document.getElementById('public-updated').textContent='更新于 '+new Date(data.now*1000).toLocaleTimeString('zh-CN');
    const cards=rows.map(machine=>{
      const card=node('article','public-machine'+(machine.online?'':' offline'));
      const head=node('div','public-machine-head');head.append(node('h2','',machine.name),node('span','status '+(machine.online?'up':'paused'),machine.online?'在线':'离线'));card.append(head);
      const metrics=node('div','public-metrics');
      for(const [key,label] of [['cpu','CPU'],['mem','内存'],['disk','磁盘']]){
        const value=machine.online&&typeof machine[key]==='number'&&Number.isFinite(machine[key])?Math.min(100,Math.max(0,machine[key])):null;
        const metric=node('div','public-metric');metric.append(node('span','muted',label),node('strong',value!==null&&value>=90?'down':'',value===null?'—':value.toFixed(1)+'%'));
        const bar=node('div','public-bar'),fill=node('span','');fill.style.width=(value||0)+'%';bar.append(fill);metric.append(bar);metrics.append(metric);
      }
      card.append(metrics);return card;
    });
    grid.replaceChildren(...cards);grid.classList.remove('stale');
    document.getElementById('public-empty').hidden=rows.length>0;
    publicHasData=true;error.hidden=true;
  }catch{error.textContent='暂时无法更新状态'+(publicHasData?'，以下为最后一次收到的数据':'，正在重试');error.hidden=false;grid.classList.add('stale')}
  finally{publicLoading=false}
}
refreshPublic();setInterval(()=>{if(!document.hidden)refreshPublic()},2000);
window.addEventListener('online',refreshPublic);
