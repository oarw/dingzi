import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source=readFileSync(new URL('../internal/server/web/board.js',import.meta.url),'utf8');
function panel(){
  const elements=new Map(),timers=new Map();let timerID=0;
  function element(id){
    if(!elements.has(id)){
      const classes=new Set(),attrs=new Map();
      elements.set(id,{innerHTML:'',textContent:'',hidden:true,dataset:{},addEventListener(){},contains(){return false},querySelectorAll(){return []},
        setAttribute(k,v){attrs.set(k,v)},getAttribute(k){return attrs.get(k)},
        classList:{add(c){classes.add(c)},remove(c){classes.delete(c)},contains(c){return classes.has(c)}}});
    }
    return elements.get(id);
  }
  const filters=['online','warn','quota','offline'].map(key=>{const el=element('filter-'+key);el.dataset.fleetFilter=key;return el});
  const context=vm.createContext({document:{activeElement:null,hidden:false,getElementById:element,querySelectorAll:()=>filters,addEventListener(){}},
    window:{addEventListener(){}},location:{search:'?still',hash:''},URLSearchParams,AbortSignal,Date,
    setInterval(){},setTimeout(fn){timers.set(++timerID,fn);return timerID},clearTimeout(id){timers.delete(id)},
    icon:()=>'',tool:()=>'',fetch:async()=>{throw new Error('offline')}});
  vm.runInContext(source,context);
  return {context,element,timers,run:code=>vm.runInContext(code,context)};
}
const machine=(extra={})=>({id:1,name:'测试机器',online:true,quota_bytes:1000,quota_mode:'sum',net_in_total:1300,net_out_total:0,cpu:10,mem:20,swap:0,disk:30,load:[0,0,0],uptime:60,...extra});
const row=(extra={})=>({id:1,name:'测试机器',online:true,quota:1000,quota_mode:'sum',traffic_in:1300,traffic_out:0,cpu:10,mem:20,swap:0,disk:30,load1:0,load5:0,load15:0,uptime:60,...extra});

test('quota text retains real usage; thresholds and billing modes are distinct',()=>{
  const p=panel();
  for(const [used,percent,tone,label] of [[796,79.6,'',''],[800,80,'caution','临近配额'],[949,94.9,'caution','临近配额'],[950,95,'warn','配额将耗尽'],[1000,100,'warn','配额已用尽'],[1300,130,'warn','配额超额']]){
    p.context.sample=machine({net_in_total:used});
    const state=p.run('quotaState(sample)'),markup=p.run('quota(sample)');
    assert(Math.abs(state.percent-percent)<1e-9);assert.equal(state.tone,tone);assert.equal(state.label,label);
    assert(markup.includes(percent+'% / '));assert((markup.match(/class="seg on/g)||[]).length<=20);
    if(used>1000)assert(markup.includes('超出 300B'));
  }
  for(const [mode,expected] of [['sum',100],['out',40],['max',60]]){
    p.context.sample=machine({net_in_total:600,net_out_total:400,quota_mode:mode});
    assert.equal(p.run('quotaState(sample).percent'),expected);
  }
  p.context.sample=machine({quota_bytes:0});
  assert.equal(p.run('quotaWarning(sample)'),false);
  assert(p.run('quota(sample)').includes('未设配额'));
});

test('quota risks are visible in cards and a separate, filterable summary',async()=>{
  const p=panel();
  p.context.fetch=async()=>({ok:true,json:async()=>({now:Date.now()/1000,servers:[row(),row({id:2,online:false}),row({id:3,traffic_in:0,cpu:95})]})});
  await p.run('load()');
  assert(p.element('grid').innerHTML.includes('配额超额'));
  assert.equal(p.element('n-quota').textContent,1);assert.equal(p.element('n-warn').textContent,1);assert.equal(p.element('n-off').textContent,1);
  p.run('setFleetFilter("quota")');
  assert.equal((p.element('grid').innerHTML.match(/<article/g)||[]).length,1);
  assert.equal(p.element('filter-quota').getAttribute('aria-pressed'),'true');
  assert.equal(p.element('n-off').textContent,1,'summary must still describe the whole fleet');
  p.run('setFleetFilter("offline")');
  assert(p.element('grid').innerHTML.includes('is-off'));
  assert(!p.element('grid').innerHTML.includes('配额超额'),'offline stale usage is not treated as a live quota alert');
  p.run('setFleetFilter("all")');
  assert.equal((p.element('grid').innerHTML.match(/<article/g)||[]).length,3);
});

test('connection state ages, fails, preserves last data, and recovers',async()=>{
  const p=panel();
  const success=async()=>({ok:true,json:async()=>({now:Date.now()/1000,servers:[row()]})});
  p.context.fetch=success;await p.run('load()');
  assert.equal(p.element('data-indicator').dataset.state,'live');
  [...p.timers.values()][0]();
  assert.equal(p.element('data-indicator').dataset.state,'stale');
  assert(p.element('grid').classList.contains('stale'));
  p.context.fetch=async()=>{throw new Error('offline')};await p.run('load()');
  assert.equal(p.element('data-indicator').dataset.state,'offline');
  assert(p.element('grid').innerHTML.includes('测试机器'));
  assert.equal(p.element('fleet-empty').hidden,true);
  p.context.fetch=success;await p.run('load()');
  assert.equal(p.element('data-indicator').dataset.state,'live');
  assert.equal(p.element('grid').classList.contains('stale'),false);
});

test('no-match, first-load failure and a genuinely empty fleet stay distinct',async()=>{
  const p=panel();
  await p.run('load()');p.run('render([])');
  assert.equal(p.element('fleet-empty').hidden,true,'login completion must not turn an error into onboarding');
  p.context.fetch=async()=>({ok:true,json:async()=>({now:Date.now()/1000,servers:[row({traffic_in:0})]})});
  await p.run('load()');p.run('setFleetFilter("quota")');
  assert.equal(p.element('fleet-filter-empty').hidden,false);assert.equal(p.element('fleet-empty').hidden,true);
  p.context.fetch=async()=>({ok:true,json:async()=>({now:Date.now()/1000,servers:[]})});
  await p.run('load()');
  assert.equal(p.element('fleet-empty').hidden,false);assert.equal(p.element('fleet-filter-empty').hidden,true);
});

test('terminal callbacks cannot cross a close and reopen boundary',()=>{
  const p=panel(),sockets=[],terminals=[];
  Object.assign(p.context,{
    AUTHED:true,TextEncoder,
    getComputedStyle:()=>({getPropertyValue:()=>''}),
    Terminal:class {
      constructor(){this.cols=80;this.rows=24;this.output=[];terminals.push(this)}
      loadAddon(){} open(){} focus(){} dispose(){}
      write(data){this.output.push(data)} writeln(data){this.output.push(data)}
      onData(fn){this.data=fn} onResize(fn){this.resize=fn}
    },
    FitAddon:{FitAddon:class {fit(){}}},
    WebSocket:class {
      constructor(){this.readyState=1;this.sent=[];sockets.push(this)}
      send(data){this.sent.push(data)} close(){this.readyState=3}
    }
  });
  p.context.document.documentElement={dataset:{theme:'dark'}};
  p.element('term-wrap').showModal=()=>{};p.element('term-wrap').close=()=>{};
  p.run('openTerm(1,"first")');
  const old=sockets[0],first=terminals[0];
  const lateMessage=old.onmessage,lateError=old.onerror,lateClose=old.onclose;
  p.run('openTerm(2,"second")');
  const current=sockets[1],second=terminals[1];
  assert.equal(old.onmessage,null);assert.equal(old.onerror,null);assert.equal(old.onclose,null);
  lateMessage({data:new Uint8Array([65]).buffer});
  lateMessage({data:JSON.stringify({type:'ready',shell:'old shell'})});
  lateError();lateClose();first.data('stale input');first.resize({cols:1,rows:1});
  assert.equal(second.output.length,0);assert.equal(current.sent.length,0);
  assert.equal(p.element('term-shell').textContent,'');
  current.onmessage({data:new Uint8Array([66]).buffer});second.data('current input');
  assert.equal(second.output.length,1);assert.equal(current.sent.length,1);
});
