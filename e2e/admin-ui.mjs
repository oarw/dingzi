// Browser-only response fixtures. No synthetic machines or metrics are saved on the panel.
import assert from 'node:assert/strict';
import {join} from 'node:path';
import {writeFile} from 'node:fs/promises';

// A late session response must not close a menu the user has already opened.
export async function verifyMenuStartup(page,base){
  let releaseSession;
  const sessionGate=new Promise(resolve=>{releaseSession=resolve});
  await page.route('**/api/v1/session',async route=>{await sessionGate;await route.continue()});
  try{
    await page.goto(base+'/admin/');
    await page.locator('#data-indicator[data-state=live]').waitFor();
    await page.locator('#nav-toggle').click();
    const sessionResponse=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/v1/session');
    releaseSession();await sessionResponse;
    await page.evaluate(()=>new Promise(requestAnimationFrame));
    assert.equal(await page.locator('#nav-toggle').getAttribute('aria-expanded'),'true','late initial navigation closed an open menu');
    await page.getByRole('button',{name:'退出登录',exact:true}).click();
    await page.waitForURL(base+'/admin/login');
    assert.equal((await page.request.get(base+'/api/v1/settings')).status(),401);
  }finally{
    releaseSession();await page.unroute('**/api/v1/session');
  }
}

export async function verifyAdminUI(page,base,machine,out){
  const defaults={...machine,online:true,cpu:10,mem:20,swap:0,disk:30,quota:1000,quota_mode:'sum',traffic_in:0,traffic_out:0};
  const fixtures=[
    {...defaults,id:101,name:'验收 · 配额 80%',traffic_in:800},
    {...defaults,id:102,name:'验收 · 配额 100%',traffic_in:1000},
    {...defaults,id:103,name:'验收 · 配额 130%',traffic_in:1300},
    {...defaults,id:104,name:'验收 · CPU 告警',cpu:95},
    {...defaults,id:105,name:'验收 · 离线',online:false},
    {...defaults,id:106,name:'验收 · 正常'}
  ];
  let rows=fixtures,unavailable=false;
  const layoutMeasurements=[];
  await page.route('**/api/v1/servers',route=>unavailable?route.abort():route.fulfill({json:{servers:rows,now:Date.now()/1000,terminal_enabled:false}}));
  try{
    await page.goto(base+'/admin/?still=1#fleet');
    await page.getByRole('link',{name:'验收 · 配额 130%',exact:true}).waitFor();
    const over=page.locator('.mc').filter({has:page.getByRole('link',{name:'验收 · 配额 130%',exact:true})});
    assert.match(await over.locator('.quota-value').innerText(),/^130%/);
    assert.equal(await over.locator('.mc-head .quota-flag').innerText(),'配额超额');
    assert.match(await over.locator('.quota-note').innerText(),/超出 300B/);
    assert.equal(await over.locator('.quota .seg.on').count(),20);
    assert.equal(await page.locator('#n-quota').innerText(),'3');
    assert.equal(await page.locator('#n-warn').innerText(),'1');
    assert.equal(await page.locator('#n-off').innerText(),'1');

    for(const [width,height,theme] of [[1440,1000,'light'],[1024,900,'dark'],[768,1000,'light'],[390,844,'dark'],[320,812,'light']]){
      await page.setViewportSize({width,height});await page.emulateMedia({colorScheme:theme});
      await page.evaluate(()=>load());
      assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'fleet overflow at '+width);
      assert(await page.locator('.mod-k').first().evaluate(el=>parseFloat(getComputedStyle(el).fontSize)>=12),'metric label is too small');
      assert.equal(await page.locator('#data-indicator').evaluate(el=>getComputedStyle(el).animationName),'none');
      layoutMeasurements.push({width,height,theme,firstCardTop:(await page.locator('.mc').first().boundingBox()).y});
      if(width<=760){
        assert.equal(await page.locator('#nav-toggle').getAttribute('aria-expanded'),'false');
        assert.equal(await page.locator('#admin-navigation').isVisible(),false);
        assert((await page.locator('.mc').first().boundingBox()).y<260,'first card is below compact mobile header');
        await page.locator('#nav-toggle').click();
        await page.keyboard.press('Escape');
        assert.equal(await page.locator('#nav-toggle').getAttribute('aria-expanded'),'false');
        assert(await page.locator('#nav-toggle').evaluate(el=>document.activeElement===el),'menu did not restore keyboard focus');
        await page.locator('#nav-toggle').click();
        await page.locator('[data-fleet-filter=quota]').click();
        assert.equal(await page.locator('#admin-navigation').isVisible(),false,'same-page summary click kept menu open');
        await page.locator('#clear-fleet-filter').click();
        await page.locator('#nav-toggle').click();
        await page.getByRole('link',{name:'面板设置',exact:true}).click();
        await page.locator('#settings-form').waitFor();
        assert.equal(await page.locator('#admin-navigation').isVisible(),false,'selecting a page kept mobile menu expanded');
      }
      await page.locator('[data-fleet-filter=quota]').click();
      await page.locator('#fleet-view').waitFor();
      assert.equal(await page.locator('.mc').count(),3);
      assert.match(await page.locator('#fleet-filter-status').innerText(),/3 台 \/ 共 6 台/);
      await page.locator('[data-fleet-filter=offline]').click();
      assert.equal(await page.locator('.mc').count(),1);
      assert.equal(await page.locator('#n-quota').innerText(),'3','filter changed global summary counts');
      await page.locator('#clear-fleet-filter').click();
      assert.equal(await page.locator('.mc').count(),6);
      await page.evaluate(()=>document.fonts.ready);
      await page.screenshot({path:join(out,'admin-clarity-'+width+'-'+theme+'.png'),fullPage:true});
    }
    rows=[{...defaults,id:106,name:'验收 · 正常'}];await page.evaluate(()=>load());
    await page.locator('[data-fleet-filter=quota]').click();
    assert(await page.locator('#fleet-filter-empty').isVisible());
    assert.equal(await page.locator('#fleet-empty').isVisible(),false);

    unavailable=true;await page.evaluate(()=>load());
    assert.equal(await page.locator('#data-indicator').getAttribute('data-state'),'offline');
    assert.equal(await page.locator('#fleet-empty').isVisible(),false);
    assert.equal(await page.locator('#fleet-filter-empty').isVisible(),false);
    await page.goto(base+'/admin/?still=1&unavailable=1#fleet');
    await page.getByText(/面板连接中断/).waitFor();
    assert.equal(await page.locator('#fleet-empty').isVisible(),false,'failed first load displayed onboarding');

    unavailable=false;rows=[];await page.evaluate(()=>load());
    assert(await page.locator('#fleet-empty').isVisible());
    assert.equal(await page.locator('#data-indicator').getAttribute('data-state'),'live');
    assert.match(await page.getByRole('link',{name:'查看探针接入步骤'}).getAttribute('href'),/^https:\/\/github.com\/oarw\/dingzi/);
    await page.getByText('已安装但未出现？',{exact:true}).click();
    assert(await page.getByText('核对探针配置中的面板地址与注册密钥。',{exact:true}).isVisible());
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'empty-state overflow');
    await page.screenshot({path:join(out,'admin-clarity-empty-mobile.png'),fullPage:true});
    await writeFile(join(out,'admin-clarity-layout.json'),JSON.stringify(layoutMeasurements,null,2));
  }finally{
    await page.unroute('**/api/v1/servers');
  }
}
