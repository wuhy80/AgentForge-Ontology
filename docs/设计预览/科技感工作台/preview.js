const shapes={
  brand:'<path d="m12 2 9 5v10l-9 5-9-5V7Z"/><path d="m3 7 9 5 9-5M12 12v10M7.5 4.5l9 5v5"/>',
  box:'<path d="m12 3 8 4.5v9L12 21l-8-4.5v-9Z"/><path d="m4 7.5 8 4.5 8-4.5M12 12v9"/>',
  network:'<rect x="9" y="2" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="16" y="16" width="6" height="6" rx="1"/><path d="M12 8v4M5 16v-4h14v4"/>',
  dashboard:'<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
  grid:'<path d="M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z"/>',
  list:'<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  link:'<circle cx="5" cy="5" r="2"/><circle cx="5" cy="19" r="2"/><circle cx="19" cy="8" r="2"/><path d="M5 7v10M7 18l10-9"/>',
  workflow:'<rect x="3" y="3" width="6" height="6" rx="1"/><rect x="15" y="15" width="6" height="6" rx="1"/><path d="M6 9v6a3 3 0 0 0 3 3h6M15 6h3v3"/>',
  code:'<path d="m8 6-6 6 6 6M16 6l6 6-6 6M14 3l-4 18"/>',
  search:'<circle cx="10.8" cy="10.8" r="7"/><path d="m16 16 5 5"/>',
  folder:'<path d="M3 7a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10H3Z"/>',
  database:'<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v14c0 4 16 4 16 0V5M4 12c0 4 16 4 16 0"/>',
  shield:'<path d="m12 3 8 3v7c0 5-8 8-8 8s-8-3-8-8V6Z"/><path d="m8 12 3 3 5-5"/>',
  help:'<circle cx="12" cy="12" r="9"/><path d="M9 9a3 3 0 1 1 5 2c-2 1-2 2-2 3M12 17h.01"/>',
  info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7h.01"/>',
  arrow:'<path d="M5 12h14m-6-6 6 6-6 6"/>',
  left:'<path d="m15 5-7 7 7 7"/>',right:'<path d="m9 5 7 7-7 7"/>',
  chevron:'<path d="m7 10 5 5 5-5"/>',chevrons:'<path d="m9 7 3-3 3 3M9 17l3 3 3-3"/>',
  refresh:'<path d="M20 10a8 8 0 0 0-14-5L3 8m0-5v5h5M4 14a8 8 0 0 0 14 5l3-3m0 5v-5h-5"/>',
  filter:'<path d="M4 6h16M7 12h10M10 18h4"/>',plus:'<path d="M5 12h14M12 5v14"/>',minus:'<path d="M5 12h14"/>',
  expand:'<path d="M9 3H3v6M15 3h6v6M3 15v6h6M15 21h6v-6"/>',
  external:'<path d="M14 3h7v7m0-7L10 14M10 4H4v16h16v-6"/>',
  more:'<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
  table:'<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 10h18M9 10v10"/>',play:'<path d="m8 4 12 8-12 8Z"/>'
}
function icon(name){return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">'+(shapes[name]||shapes.box)+'</svg>'}
document.querySelectorAll('[data-icon]').forEach(el=>el.innerHTML=icon(el.dataset.icon))
document.querySelectorAll('.nav-item').forEach(el=>{const label=el.querySelector('span:not([data-icon])');if(label)el.setAttribute('aria-label',label.textContent)})
document.querySelector('.topbar-actions').prepend(document.querySelector('.category-button'))

const models=[
  {id:'notes',name:'笔记',kind:'schema',group:'内容资产',description:'组织知识内容，连接来源、标签与引用。',hint:'知识内容的核心对象',count:8},
  {id:'knowledge',name:'知识库',kind:'schema',group:'知识组织',description:'归集主题知识，组织可持续积累的知识空间。',hint:'组织和归集知识',count:6},
  {id:'tags',name:'标签',kind:'schema',group:'知识组织',description:'用清晰的主题标记知识，让内容易于发现。',hint:'关联主题与分类',count:4},
  {id:'references',name:'引用记录',kind:'schema',group:'内容资产',description:'记录知识之间的引用与关联。',hint:'追溯内容之间的引用',count:7},
  {id:'sources',name:'来源',kind:'schema',group:'内容资产',description:'维护知识的来源与原始材料。',hint:'追溯原始知识来源',count:5},
  {id:'attachments',name:'附件',kind:'schema',group:'内容资产',description:'归集与知识内容关联的文件和资料。',hint:'管理关联文件与资料',count:5},
  ...['笔记标记标签','笔记产生引用','笔记包含附件','知识库收录笔记','笔记引用来源','笔记引用笔记','知识库收录来源'].map((name,i)=>({id:'link-'+i,name,kind:'link',group:'业务关系',description:'定义知识对象之间的双向业务关联。',count:2})),
  ...['笔记状态','来源类型','标签类型'].map((name,i)=>({id:'enum-'+i,name,kind:'enum',group:'语义定义',description:'统一知识内容的业务取值和语义。',count:3})),
  ...['生成内容摘要','智能归类'].map((name,i)=>({id:'action-'+i,name,kind:'action',group:'计算能力',description:'面向知识对象的计算操作。',count:2})),
  ...['文本摘要','规范标题'].map((name,i)=>({id:'function-'+i,name,kind:'function',group:'计算能力',description:'可被动作引用的内容计算能力。',count:2})),
]
const kindNames={schema:'对象模型',link:'关系',enum:'枚举',action:'动作',function:'函数'}
const kindIcons={schema:'box',link:'link',enum:'list',action:'workflow',function:'code'}
let selected='notes',mode='relations',filter='',search='',detailTab='properties',zoom=1,page='ontology'
const properties=[['T','标题','文本'],['≡','内容摘要','长文本'],['◇','状态','笔记状态'],['↗','来源地址','文本'],['#','阅读次数','整数'],['◷','创建时间','时间'],['◷','更新时间','时间'],['T','作者','文本']]
const positions=[['knowledge',6,16],['notes',38,42],['tags',70,14],['references',70,47],['sources',6,74],['attachments',70,77]]
const edges=[
  ['knowledge','notes','M300 154 C350 154 330 322 380 322','收录笔记',33.5,35],
  ['notes','tags','M620 322 C672 322 650 143 700 143','标记标签',66,31],
  ['notes','references','M620 322 C665 322 654 355 700 355','产生引用',67.5,50.5],
  ['notes','attachments','M620 322 C675 322 642 550 700 550','包含附件',66,71],
  ['sources','notes','M300 531 C350 531 330 322 380 322','引用来源',33.5,68],
  ['knowledge','sources','M180 198 L180 481','收录来源',18,55],
  ['notes','notes','M455 273 C403 162 612 162 560 273','引用笔记',50.5,30],
]
const dependencyPositions=[['link-0',6,19],['notes',38,42],['enum-0',70,16],['action-0',70,48],['knowledge',6,73],['function-0',70,78]]
const dependencyEdges=[
  ['link-0','notes','M300 172 C350 172 335 322 380 322','起点对象',33,35],
  ['notes','enum-0','M620 322 C672 322 655 154 700 154','引用枚举',66,31],
  ['action-0','notes','M700 362 C665 362 660 322 620 322','作用对象',66.5,51],
  ['action-0','function-0','M820 400 L820 508','绑定函数',82,70],
  ['knowledge','notes','M300 524 C350 524 330 322 380 322','关联对象',33,68],
]
function renderDirectory(){
  const list=document.getElementById('directory-list')
  document.querySelector('.directory .quiet-number').textContent = models.filter(model => (!filter || model.kind === filter) && (!search || (model.name + model.description).includes(search))).length
  list.innerHTML=Object.entries(kindNames).map(([kind,label])=>{
    const rows=models.filter(m=>m.kind===kind&&(!filter||filter===kind)&&(!search||(m.name+m.description).includes(search)))
    if(!rows.length)return ''
    return '<section class="directory-group"><h3>'+label+'<span>'+rows.length+'</span></h3>'+rows.map(m=>'<button class="directory-entry '+(m.id===selected?'selected':'')+'" data-select="'+m.id+'"><span>'+icon(kindIcons[kind])+'</span><span>'+m.name+'</span><span>'+(m.id===selected?'<i class="entry-dot"></i>':m.kind==='schema'?m.count:'')+'</span></button>').join('')+'</section>'
  }).join('')||'<p style="padding:22px 10px;color:var(--muted);font-size:11px">没有匹配的示例定义</p>'
  list.querySelectorAll('[data-select]').forEach(el=>el.onclick=()=>select(el.dataset.select))
}
function renderGraph(){
  const world=document.getElementById('graph-world')
  const currentPositions=mode==='relations'?positions:dependencyPositions,currentEdges=mode==='relations'?edges:dependencyEdges
  const highlighted=currentEdges.filter(e=>e[0]===selected||e[1]===selected)
  let svg='<svg class="graph-edges" viewBox="0 0 1000 650" preserveAspectRatio="none" aria-hidden="true"><defs><marker id="arrow-muted" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="5" markerHeight="5" orient="auto"><path d="M0 0 10 5 0 10Z" fill="var(--edge)"/></marker><marker id="arrow-active" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="5" markerHeight="5" orient="auto"><path d="M0 0 10 5 0 10Z" fill="var(--edge-active)"/></marker></defs>'
  currentEdges.forEach(e=>{const active=highlighted.includes(e);svg+='<path d="'+e[2]+'" class="graph-edge '+(active?'active ':'')+(mode==='dependencies'?'dependency-edge':'')+'" marker-end="url(#arrow-'+(active?'active':'muted')+')"/>'})
  svg+='</svg>'
  world.innerHTML=svg+currentEdges.map(e=>'<span class="edge-label '+(highlighted.includes(e)?'active':'')+'" style="left:'+e[4]+'%;top:'+e[5]+'%">'+e[3]+'</span>').join('')+currentPositions.map(([id,x,y])=>{
    const m=models.find(r=>r.id===id)
    return '<button class="graph-node '+(id===selected?'selected':'')+'" data-select="'+id+'" style="left:'+x+'%;top:'+y+'%" aria-label="选择'+m.name+'"><div><div class="node-title"><span class="node-icon '+m.kind+'">'+icon(kindIcons[m.kind])+'</span>'+m.name+'</div><p class="node-description">'+(m.hint||m.description)+'</p></div><div class="node-footer"><span>'+m.count+' 个'+(m.kind==='schema'?'属性':'定义项')+'</span><span class="small-status"><i></i>已生效</span></div></button>'
  }).join('')
  world.querySelectorAll('[data-select]').forEach(el=>el.onclick=()=>select(el.dataset.select))
  requestAnimationFrame(()=>{const stage=document.querySelector('.graph-stage'),node=world.querySelector('.graph-node.selected');if(node&&stage.clientWidth<world.scrollWidth)stage.scrollLeft=Math.max(0,node.offsetLeft+node.offsetWidth/2-stage.clientWidth/2)})
  document.getElementById('graph-count').textContent=mode==='relations'?'6 个对象 · 7 条关系':'6 个示例定义 · 5 条引用'
  document.querySelector('.graph-context').innerHTML='<span class="context-orbit"></span>个人知识库<span class="context-slash">/</span>'+(mode==='relations'?'对象关系':'定义依赖')
  document.querySelector('.legend').innerHTML='<span><i class="line-sample"'+(mode==='dependencies'?' style="border-top-style:dashed"':'')+'></i>'+(mode==='relations'?'业务关系':'定义引用')+'</span><span><i class="node-sample"></i>'+(mode==='relations'?'对象模型':'本体定义')+'</span>'
}
function renderDetail(){
  const record=models.find(m=>m.id===selected)
  document.getElementById('detail-name').textContent=record.name
  document.getElementById('detail-description').textContent=record.description
  document.getElementById('detail-group').textContent=record.group
  document.getElementById('property-count').textContent=record.count
  document.querySelector('.type-label').innerHTML=icon(kindIcons[record.kind])+kindNames[record.kind]
  document.querySelector('.detail-symbol').innerHTML=icon(kindIcons[record.kind])
  const rows=record.id==='notes'?properties:record.kind==='enum'?[['◇','未整理','待处理'],['◇','已整理','可使用'],['◇','已归档','已完成']]:[['T','名称','文本'],['≡','说明','长文本'],['◷','创建时间','时间'],['◷','更新时间','时间']].slice(0,record.count)
  const relationships = (mode === 'relations' ? edges : dependencyEdges).filter(([from,to]) => from === selected || to === selected)
  document.getElementById('relation-count').textContent = relationships.length
  document.getElementById('detail-content').innerHTML=detailTab==='properties'?rows.map(([symbol,name,type])=>'<div class="property-row"><span>'+symbol+'</span>'+name+(type==='笔记状态'?'<span class="enum-value">'+type+' ↗</span>':'<code>'+type+'</code>')+'</div>').join(''):relationships.map(([from,to,,name]) => {
    const source = models.find(model => model.id === from)
    const target = models.find(model => model.id === to)
    return '<div class="relation-row">'+name+'<span>'+source.name+' → '+target.name+'</span></div>'
  }).join('') || '<p class="detail-description">当前视图没有此定义的关联。</p>'
}
function select(id) {
  const focused = document.activeElement
  const origin = focused?.closest('.graph-world, .directory-list')
  selected = id
  const record = models.find(model => model.id === id)
  if (['enum', 'action', 'function'].includes(record.kind)) mode = 'dependencies'
  renderDirectory()
  renderGraph()
  renderDetail()
  syncTabs('[data-mode]', 'mode', mode, 'preview-graph-panel')
  if (origin && focused.dataset.select === id) origin.querySelector('[data-select="'+id+'"]')?.focus({ preventScroll: true })
}
function setPane(pane) {
  document.getElementById('ontology-workbench').dataset.pane = pane
  document.querySelectorAll('#preview-panes button').forEach(button => button.setAttribute('aria-pressed', button.dataset.pane === pane))
}
document.querySelectorAll('#preview-panes button').forEach(button => {
  button.onclick = () => setPane(button.dataset.pane)
})
function syncTabs(selector, key, value, panelId) {
  const panel = document.getElementById(panelId)
  panel.setAttribute('role', 'tabpanel')
  panel.tabIndex = 0
  document.querySelectorAll(selector).forEach(tab => {
    const active = tab.dataset[key] === value
    tab.id = panelId + '-' + tab.dataset[key]
    tab.tabIndex = active ? 0 : -1
    tab.setAttribute('aria-controls', panelId)
    tab.setAttribute('aria-selected', active)
    if (active) panel.setAttribute('aria-labelledby', tab.id)
  })
}
function bindTabs(selector, key, panelId, initial, activate) {
  const tabs = Array.from(document.querySelectorAll(selector))
  syncTabs(selector, key, initial, panelId)
  tabs.forEach((tab, index) => {
    tab.onclick = () => {
      activate(tab.dataset[key])
      syncTabs(selector, key, tab.dataset[key], panelId)
    }
    tab.onkeydown = event => {
      const destination = { ArrowLeft: (index + tabs.length - 1) % tabs.length, ArrowRight: (index + 1) % tabs.length, Home: 0, End: tabs.length - 1 }[event.key]
      if (destination === undefined) return
      event.preventDefault()
      tabs[destination].click()
      tabs[destination].focus()
    }
  })
}
function syncFilters() {
  document.querySelectorAll('[data-filter]').forEach(button => button.setAttribute('aria-pressed', button.dataset.filter === filter))
}

function showPage(target){
  if(target==='overview'){toast('本轮先预览本体全景和数据探索两个典型工作区');return}
  page=target
  const isQuery=page==='query'
  document.getElementById('ontology-workbench').hidden=isQuery
  document.getElementById('query-workbench').hidden=!isQuery
  document.querySelector('.metrics').hidden=isQuery
  document.getElementById('preview-panes').hidden=isQuery
  document.getElementById('page-title').textContent=isQuery?'数据探索':'本体全景'
  document.getElementById('breadcrumb-current').textContent=isQuery?'数据探索':'本体全景'
  document.getElementById('page-description').textContent=isQuery?'用业务语言提问，让知识之间的联系成为答案。':'浏览当前分类的对象关系与定义依赖。'
  document.getElementById('refresh').lastElementChild.textContent=isQuery?'刷新目录':'刷新本体'
  document.querySelectorAll('[data-page]').forEach(el=>el.classList.toggle('active',el.dataset.page===page))
  updateAddress()
}
let toastTimer
function toast(message){const el=document.getElementById('toast');el.textContent=message;el.classList.add('visible');clearTimeout(toastTimer);toastTimer=setTimeout(()=>el.classList.remove('visible'),2800)}
function setTheme(theme){document.documentElement.dataset.theme=theme;document.querySelectorAll('[data-theme-option]').forEach(el=>el.setAttribute('aria-pressed',el.dataset.themeOption===theme));updateAddress()}
function updateAddress(){const url=new URL(location.href);url.searchParams.set('theme',document.documentElement.dataset.theme);url.searchParams.set('page',page);history.replaceState(null,'',url)}
document.querySelectorAll('[data-theme-option]').forEach(el=>el.onclick=()=>setTheme(el.dataset.themeOption))
document.querySelectorAll('[data-page]').forEach(el=>el.onclick=()=>showPage(el.dataset.page))
document.querySelectorAll('[data-filter]').forEach(button => {
  button.onclick = () => {
    filter = filter === button.dataset.filter ? '' : button.dataset.filter
    syncFilters()
    renderDirectory()
    setPane('directory')
  }
})
bindTabs('[data-mode]', 'mode', 'preview-graph-panel', mode, view => { mode = view; renderGraph(); renderDetail() })
bindTabs('[data-detail-tab]', 'detailTab', 'detail-content', detailTab, view => { detailTab = view; renderDetail() })
document.querySelectorAll('[data-demo]').forEach(el=>el.onclick=()=>toast(el.dataset.demo))
document.getElementById('directory-search').oninput=el=>{search=el.target.value.trim();renderDirectory()}
document.getElementById('clear-filter').onclick=()=>{filter='';search='';document.getElementById('directory-search').value='';syncFilters();renderDirectory();toast('已显示全部示例定义')}
document.getElementById('global-search').onclick=()=>{showPage('ontology');setPane('directory');document.getElementById('directory-search').focus()}
document.addEventListener('keydown',event=>{if((event.ctrlKey||event.metaKey)&&event.key.toLowerCase()==='k'){event.preventDefault();document.getElementById('global-search').click()}})
function setZoom(value){zoom=Math.min(1.4,Math.max(.6,value));document.getElementById('graph-world').style.setProperty('--zoom',zoom);document.getElementById('zoom-value').textContent=Math.round(zoom*100)+'%'}
document.getElementById('zoom-in').onclick=()=>setZoom(zoom+.1)
document.getElementById('zoom-out').onclick=()=>setZoom(zoom-.1)
document.getElementById('fit-graph').onclick=()=>setZoom(1)
document.getElementById('view-data').onclick=()=>{showPage('query');toast('已切换到数据探索示例')}
document.getElementById('view-definition').onclick=()=>toast('正式改造时从这里进入现有定义编辑页')
document.getElementById('refresh').onclick=()=>toast('已刷新示例内容；未请求业务服务')
document.getElementById('run-query').onclick=()=>{document.getElementById('query-result-status').innerHTML='<span class="status-dot"></span>已显示示例结果';toast('已显示本地示例结果，未执行真实查询')}
const titles=['本体建模的核心概念','如何组织个人知识库','理解对象与业务关系','知识引用与来源追溯','OntologySQL 查询实践','让知识成为可调用的能力']
document.getElementById('result-rows').innerHTML=titles.map((title,i)=>'<tr><td>'+String(i+1).padStart(2,'0')+'</td><td>'+title+'</td><td><span class="result-badge"><span class="status-dot"></span>已整理</span></td><td style="color:var(--muted);font-size:10px">2026-11-08 '+(14-i)+':'+String(38-i*3).padStart(2,'0')+'</td><td>'+icon('right')+'</td></tr>').join('')
const initial=new URLSearchParams(location.search),initialTheme=initial.get('theme')==='light'?'light':'deep',initialPage=initial.get('page')==='query'?'query':'ontology'
renderDirectory();renderGraph();renderDetail();setTheme(initialTheme);showPage(initialPage)
