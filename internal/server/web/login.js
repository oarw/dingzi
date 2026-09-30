"use strict";
if(new URLSearchParams(location.search).has('github_error')){
  const error=document.getElementById('github-error');
  const reason=new URLSearchParams(location.search).get('github_error');
  error.textContent=reason==='denied'?'此 GitHub 账号未获管理员授权，请使用已授权账号或管理员密码登录。':'GitHub 登录未完成或已过期，请重试，也可以使用管理员密码登录。';
  error.hidden=false;
  history.replaceState(null,'','/admin/login');
}
document.getElementById('login-form').addEventListener('submit',async event=>{
  event.preventDefault();
  const form=event.currentTarget,button=form.querySelector('button'),error=document.getElementById('login-error');
  button.disabled=true;error.hidden=true;
  try{
    const response=await fetch('/api/v1/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:form.elements.password.value}),signal:AbortSignal.timeout(15000)});
    const data=await response.json();
    if(!response.ok)throw new Error(data.error||'登录失败，请稍后重试');
    form.reset();location.replace('/admin/');
  }catch(err){error.textContent=err instanceof TypeError?'无法连接面板，请稍后重试':err.message;error.hidden=false;button.disabled=false}
});
