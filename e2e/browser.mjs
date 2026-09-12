// Run against disposable local binaries. No production credentials or external notifications.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtemp,mkdir} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';

const toolsDir=process.env.DINGZI_TOOLS;
if(!toolsDir)throw new Error('DINGZI_TOOLS must point to a directory with node_modules/playwright');
const {chromium}=await import(pathToFileURL(join(toolsDir,'node_modules/playwright/index.mjs')));
const binaryDir=resolve(process.env.DINGZI_BIN||'.');
const out=resolve(process.env.DINGZI_REVIEW||join(tmpdir(),'dingzi-browser-review'));
await mkdir(out,{recursive:true});
const dir=await mkdtemp(join(process.env.DINGZI_TEMP||tmpdir(),'dingzi-browser-'));
const children=[];
function start(name,args){const child=spawn(join(binaryDir,name+(process.platform==='win32'?'.exe':'')),args,{windowsHide:true,stdio:['ignore','pipe','pipe']});children.push(child);return child}
const local=createServer((req,res)=>{if(req.url==='/health'){res.writeHead(204);res.end();return}req.resume();res.writeHead(204);res.end()});
await new Promise(r=>local.listen(0,'127.0.0.1',r));
const target='http://127.0.0.1:'+local.address().port;
const portProbe=createServer();await new Promise(r=>portProbe.listen(0,'127.0.0.1',r));const port=portProbe.address().port;await new Promise(r=>portProbe.close(r));
const base='http://127.0.0.1:'+port;
let browser,page;
const errors=[];
const pause=ms=>new Promise(r=>setTimeout(r,ms));
try{
  const server=start('dingzi-server',['--listen','127.0.0.1:'+port,'--data',join(dir,'panel')]);
  let output='';server.stdout.on('data',b=>output+=b);server.stderr.on('data',()=>{});
  let password,secret;
  for(let n=0;n<100;n++){password=output.match(/管理员密码:\s*(\S+)/)?.[1];secret=output.match(/Agent 密钥:\s*(\S+)/)?.[1];if(password&&secret)break;await pause(200)}
  assert(password&&secret,'panel did not produce first-run credentials');
  const agent=start('dingzi-agent',['--server',base,'--secret',secret,'--config',join(dir,'agent.yaml'),'--name','本地验证节点','--allow-terminal']);
  agent.stdout.resume();agent.stderr.resume();
  let machine;
  for(let n=0;n<150;n++){try{const d=await (await fetch(base+'/api/v1/servers')).json();machine=d.servers?.find(m=>m.online&&m.mem_total>0);if(machine)break}catch{}await pause(200)}
  assert(machine,'real agent did not report');
  browser=await chromium.launch({headless:true,executablePath:process.env.DINGZI_CHROME||(process.platform==='win32'?'C:/Program Files/Google/Chrome/Application/chrome.exe':undefined)});
  page=await browser.newPage({viewport:{width:1440,height:1050},colorScheme:'light'});
  page.on('pageerror',err=>errors.push(err.message));
  await page.goto(base);await page.getByRole('link',{name:'本地验证节点',exact:true}).waitFor();
  await page.getByRole('button',{name:'登录',exact:true}).click();
  await page.getByLabel('密码',{exact:true}).fill(password);
  await page.locator('#editor').getByRole('button',{name:'登录',exact:true}).click();await page.locator('#editor').waitFor({state:'hidden'});
  await page.getByRole('button',{name:'设置 本地验证节点',exact:true}).click();
  await page.getByLabel('名称',{exact:true}).fill('本地验证节点-已修改');
  await page.getByLabel('月度配额 (GiB，0 为不限)').fill('100');
  await page.locator('#editor').getByRole('button',{name:'保存',exact:true}).click();await page.locator('#editor').waitFor({state:'hidden'});
  await page.getByRole('link',{name:'服务监控',exact:true}).click();
  await page.getByRole('button',{name:'新建监控',exact:true}).first().click();
  await page.getByLabel('名称',{exact:true}).fill('本地 HTTP 检查');
  await page.getByLabel('执行探针',{exact:true}).selectOption(String(machine.id));
  await page.getByLabel('目标',{exact:true}).fill(target+'/health');
  await page.getByLabel('检查间隔 (秒)').fill('10');
  await page.locator('#editor').getByRole('button',{name:'保存',exact:true}).click();await page.locator('#editor').waitFor({state:'hidden'});
  await page.getByRole('button',{name:'立即检查 本地 HTTP 检查'}).click();
  await page.getByText('检查完成',{exact:true}).waitFor();
  await page.getByRole('link',{name:'通知渠道',exact:true}).click();
  await page.getByRole('button',{name:'新建渠道',exact:true}).first().click();
  await page.getByLabel('名称',{exact:true}).fill('本地测试通知');
  await page.getByLabel('Webhook 地址').fill(target+'/notifications');
  await page.locator('#editor').getByRole('button',{name:'保存',exact:true}).click();await page.locator('#editor').waitFor({state:'hidden'});
  await page.getByRole('button',{name:'发送测试通知 本地测试通知'}).click();await page.getByText('测试通知已送达',{exact:true}).waitFor();
  await page.getByRole('link',{name:'告警规则',exact:true}).click();
  await page.getByRole('button',{name:'新建规则',exact:true}).first().click();
  await page.getByLabel('名称',{exact:true}).fill('验证节点离线');
  await page.getByLabel('机器',{exact:true}).selectOption(String(machine.id));
  await page.locator('#editor').getByRole('button',{name:'保存',exact:true}).click();await page.locator('#editor').waitFor({state:'hidden'});
  for(const [name,width,height,theme] of [['desktop',1440,1050,'light'],['mobile',390,844,'dark']]){
    await page.setViewportSize({width,height});await page.emulateMedia({colorScheme:theme});
    for(const view of ['fleet','monitors','alerts','channels','events','machine/'+machine.id]){
      await page.goto(base+'/#'+view);
      if(view.startsWith('machine/')){await page.locator('#history-chart').waitFor();await page.waitForFunction(()=>document.getElementById('chart-state')?.textContent.includes('聚合采样点'))}
      else if(view==='fleet'){await page.getByRole('link',{name:'本地验证节点-已修改',exact:true}).waitFor()}
      else{await page.locator('#workspace tbody tr').first().waitFor()}
      assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'horizontal overflow '+name+'/'+view);
      if(view.startsWith('machine/')){
        const pixels=await page.locator('canvas').evaluate(c=>{const data=c.getContext('2d').getImageData(0,0,c.width,c.height).data;let nonblank=0,colored=0;for(let i=0;i<data.length;i+=4){if(data[i+3])nonblank++;if(data[i+3]>100&&Math.max(data[i],data[i+1],data[i+2])-Math.min(data[i],data[i+1],data[i+2])>30)colored++}return {nonblank,colored}});
        assert(pixels.nonblank>1000&&pixels.colored>0,'blank history chart '+JSON.stringify(pixels));
        await page.locator('#chart-metric').selectOption('network');
        await page.getByRole('button',{name:'6 小时',exact:true}).click();await page.waitForFunction(()=>document.getElementById('chart-state')?.textContent.includes('聚合采样点'));
      }
      await page.screenshot({path:join(out,name+'-'+view.replace('/','-')+'.png'),fullPage:true});
    }
    await page.goto(base+'/#channels');await page.getByRole('button',{name:'编辑 本地测试通知'}).waitFor();await page.getByRole('button',{name:'编辑 本地测试通知'}).click();
    assert(await page.locator('#editor').evaluate(el=>el.getBoundingClientRect().right<=innerWidth),'editor overflow');
    await page.screenshot({path:join(out,name+'-editor.png'),fullPage:true});await page.getByRole('button',{name:'关闭',exact:true}).click();
  }
  await page.goto(base+'/#fleet');await page.getByRole('button',{name:'退出登录',exact:true}).waitFor();await page.getByRole('button',{name:'退出登录',exact:true}).click();
  await page.getByRole('link',{name:'告警规则',exact:true}).click();await page.getByText('请登录后管理监控和通知').waitFor();
  await page.route('**/api/v1/servers',r=>r.abort());await page.goto(base+'/#fleet');await page.getByText(/面板连接中断/).waitFor();
  assert.equal(await page.locator('.mc').count(),0,'disconnection substituted demo machines');
  assert.deepEqual(errors,[],'browser runtime errors');
  console.log('PASS: real agent, login/logout, machine settings, monitor execution, notification, rule creation, desktop/mobile views, chart pixels, responsive forms, disconnect state');
  console.log('Screenshots: '+out);
}catch(err){
  console.error('Browser errors:',errors);
  if(page){console.error('Page:',await page.locator('body').innerText());await page.screenshot({path:join(out,'failure.png'),fullPage:true})}
  throw err;
}finally{
  if(browser)await browser.close();
  for(const child of children.reverse())child.kill();
  await new Promise(r=>local.close(r));
}
