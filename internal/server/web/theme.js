const html=document.documentElement;
const btns=[...document.querySelectorAll(".themer button")];
function setT(t,persist){html.dataset.theme=t;
  if(persist!==false)try{localStorage.setItem("dingzi-theme",t)}catch{}
  btns.forEach(b=>b.setAttribute("aria-pressed",String(b.dataset.t===t)))}
btns.forEach(b=>b.onclick=()=>setT(b.dataset.t));
const forced=new URLSearchParams(location.search).get("theme");
let stored=null;try{stored=localStorage.getItem("dingzi-theme")}catch{}
setT(forced||stored||"auto",!forced);
