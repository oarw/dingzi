// Disposable browser checks; intercept GitHub navigation, never use a real OAuth App.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {mkdtemp,mkdir,readFile,writeFile,rm} from 'node:fs/promises';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import {createServer} from 'node:http';
import {tmpdir} from 'node:os';

const toolsDir=process.env.DINGZI_TOOLS;
if(!toolsDir)throw new Error('DINGZI_TOOLS must point to a directory with node_modules/playwright');
const {chromium}=await import(pathToFileURL(join(toolsDir,'node_modules/playwright/index.mjs')));
const binary=join(resolve(process.env.DINGZI_BIN||'.'),'dingzi-server'+(process.platform==='win32'?'.exe':''));
const dir=await mkdtemp(join(tmpdir(),'dingzi-github-'));
const out=resolve(process.env.DINGZI_REVIEW||join(tmpdir(),'dingzi-github-review'));
await mkdir(out,{recursive:true});
const probe=createServer();await new Promise(r=>probe.listen(0,'127.0.0.1',r));
const port=probe.address().port;await new Promise(r=>probe.close(r));
const base='http://127.0.0.1:'+port;
let server,browser,password;
const pause=ms=>new Promise(r=>setTimeout(r,ms));
async function stop(){if(server&&server.exitCode===null){const exited=once(server,'exit');server.kill();await exited}server=undefined}
async function start(){
  server=spawn(binary,['--data',dir,'--listen','127.0.0.1:'+port],{windowsHide:true,stdio:['ignore','pipe','pipe']});
  let output='';server.stdout.on('data',b=>{output+=b;password=output.match(/管理员密码:\s*(\S+)/)?.[1]||password});server.stderr.resume();
  for(let n=0;n<300;n++){
    if(server.exitCode!==null)throw new Error('panel exited before becoming ready');
    try{if((await fetch(base+'/api/v1/session',{signal:AbortSignal.timeout(2000)})).ok)return}catch{}
    await pause(200);
  }
  throw new Error('panel did not become ready');
}
try{
  await start();assert(password,'no first-run password');
  browser=await chromium.launch({headless:true,executablePath:process.env.DINGZI_CHROME||(process.platform==='win32'?'C:/Program Files/Google/Chrome/Application/chrome.exe':undefined)});
  const page=await browser.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
  await page.goto(base+'/admin/login');
  assert.equal(await page.getByRole('link',{name:'使用 GitHub 登录'}).count(),0);
  await stop();
  const config=join(dir,'config.yaml');
  await writeFile(config,await readFile(config,'utf8')+'\ngithub:\n  client_id: fixture-client\n  client_secret: fixture-secret\n  callback_url: '+base+'/auth/github/callback\n  allowed_user_ids: [42]\n',{mode:0o600});
  await start();
  let authorization;
  await page.route(base+'/auth/github',async route=>{
    const response=await route.fetch({maxRedirects:0});
    assert.equal(response.status(),302);
    authorization=new URL(response.headers().location);
    await route.fulfill({response,status:200,contentType:'text/html',body:'<!doctype html><title>Local OAuth fixture</title><p>OAuth navigation intercepted.</p>'});
  });
  for(const [name,width,height,theme] of [['desktop',1280,900,'light'],['mobile',390,844,'dark']]){
    await page.setViewportSize({width,height});await page.emulateMedia({colorScheme:theme});
    await page.goto(base+'/admin/login');
    await page.getByRole('link',{name:'使用 GitHub 登录'}).waitFor();
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'login overflow');
    await page.screenshot({path:join(out,'github-'+name+'.png'),fullPage:true});
  }
  await page.getByRole('link',{name:'使用 GitHub 登录'}).click();
  await page.waitForURL(base+'/auth/github');
  assert.equal(authorization.origin,'https://github.com');
  assert.equal(authorization.searchParams.get('client_id'),'fixture-client');
  assert.equal(authorization.searchParams.get('redirect_uri'),base+'/auth/github/callback');
  assert.equal(authorization.searchParams.get('code_challenge_method'),'S256');
  assert.equal(authorization.searchParams.get('code_challenge').length,43);
  assert(!authorization.searchParams.has('client_secret'));
  await page.goto(base+'/auth/github/callback?error=access_denied&state='+authorization.searchParams.get('state'));
  await page.getByText('GitHub 登录未完成或已过期，请重试，也可以使用管理员密码登录。',{exact:true}).waitFor();
  assert.equal(new URL(page.url()).search,'','callback error should be removed from history');
  assert.equal((await page.request.get(base+'/api/v1/servers')).status(),401);
  await page.goto(base+'/admin/login?github_error=denied');
  await page.getByText('此 GitHub 账号未获管理员授权，请使用已授权账号或管理员密码登录。',{exact:true}).waitFor();
  await page.screenshot({path:join(out,'github-denied-mobile.png'),fullPage:true});
  await page.getByLabel('管理员密码',{exact:true}).fill(password);
  await page.getByRole('button',{name:'登录',exact:true}).click();
  await page.waitForURL(base+'/admin/');
  assert.equal((await page.request.get(base+'/api/v1/servers')).status(),200);
  assert.deepEqual(errors,[]);
  console.log('GitHub browser checks passed: disabled/configured entry, desktop/mobile, PKCE redirect, cancellation, denied account copy and password fallback.');
}finally{
  await browser?.close();await stop();
  await rm(dir,{recursive:true,force:true});
}
