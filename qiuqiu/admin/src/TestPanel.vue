<script setup lang="ts">
import {ref,reactive,computed,onMounted,onUnmounted} from 'vue';
const props=defineProps<{token:string,userId:string,devices:Record<string,any>[],topics:Record<string,any>[],policies:Record<string,any>[],channels:Record<string,any>[]}>();
const emit=defineEmits(['sent']);
const form=reactive({device_ids:[] as string[],topic_ids:[] as number[],title:'球球推送测试',body:'这是一条测试消息，请核对图标、声音与接收时间。',priority:4,business_type:'server',business_name:'',sound:'default',icon:'server',icon_url:'',content_type:'text',click_url:'',callback_url:'',group:'',tags:'',ttl:0});
const labels:Record<string,string>={accepted:'服务器已受理',pending:'等待投递',sending:'开始投递',published:'投递完成（查看通道结果）',direct_sent:'直连消息已发出',direct_unavailable:'直连不可用',waiting_receipt:'等待设备回执',receipt_timeout:'回执超时，接收未知',provider_accepted:'提供方已受理，终端接收未知',stored:'仅保存',received:'设备已收到',notification_submitted:'通知已提交系统',notification_blocked:'通知受阻',notification_suppressed:'通知未展示',opened:'用户已打开',failed:'投递失败'};
const business:Record<string,string>={server:'服务器',security:'安全',email:'邮件',task:'任务',network:'网络',custom:'自定义'};
function channelName(id:string){return props.channels.find(c=>c.id===id)?.name||({ntfy_fcm:'ntfy → FCM',app_direct:'APP直连',device:'手机回执'} as Record<string,string>)[id]||id}
function eventChannel(event:any){if(['accepted','pending','sending','published','failed'].includes(event.stage)&&!props.channels.some(c=>c.id===event.channel))return '';return channelName(event.channel)}
const snapshot=ref<any>(null),history=ref<string[]>([]),selected=ref(''),busy=ref(false),error=ref(''),connection=ref('连接中'),submitState=ref('');
let controller:AbortController|undefined,timer:ReturnType<typeof setInterval>|undefined,refreshTimer:ReturnType<typeof setTimeout>|undefined,stopped=false,refreshing=false,refreshAgain=false,cursor=0,lastActivity=Date.now();
function scheduleRefresh(){if(refreshTimer)return;refreshTimer=setTimeout(()=>{refreshTimer=undefined;if(!stopped)refresh()},250)}
const storageKey=computed(()=>'qq_tests_'+props.userId);
const advanced=ref('');
const extras=computed(()=>{try{return advanced.value?JSON.parse(advanced.value):undefined}catch{return undefined}});
const payload=computed(()=>({...form,ntfy:extras.value,delivery_policy:policy.value,tags:form.tags.split(',').map(s=>s.trim()).filter(Boolean),expires_at:form.ttl?Math.floor(Date.now()/1000)+form.ttl:0,ttl:undefined}));
const stats=computed(()=>{
 const ds=snapshot.value?.deliveries||[],rs=snapshot.value?.receipts||[];
 return {total:ds.length,received:ds.filter((d:any)=>rs.some((r:any)=>r.device_id===d.device_id&&r.stage==='received')).length,failed:ds.filter((d:any)=>d.status==='failed').length};
});
async function request(path:string,method='GET',body?:unknown){const res=await fetch('/api/v1/'+path,{method,headers:{Authorization:'Bearer '+props.token,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});const data=await res.json();if(!res.ok)throw Error(data.message||'请求失败');return data;}
async function refresh(){if(!selected.value)return;if(refreshing){refreshAgain=true;return}refreshing=true;const id=selected.value;try{const result=await request('manage/messages/'+encodeURIComponent(id));if(id===selected.value){snapshot.value=result;error.value=''}else refreshAgain=true}catch(e){if(id===selected.value)error.value=String(e)}finally{refreshing=false;if(refreshAgain&&!stopped){refreshAgain=false;await refresh()}}}
async function send(){busy.value=true;error.value='';submitState.value='正在提交请求';try{if(advanced.value)JSON.parse(advanced.value);const data=await request('manage/test','POST',payload.value);selected.value=data.message_id;history.value=[selected.value,...history.value.filter(id=>id!==selected.value)].slice(0,30);sessionStorage.setItem(storageKey.value,JSON.stringify(history.value));submitState.value='';await refresh();emit('sent')}catch(e){submitState.value='请求提交失败';error.value=String(e)}finally{busy.value=false}}
function time(n:number){return new Date(n*1000).toLocaleTimeString('zh-CN')}
function received(id:string){return (snapshot.value?.receipts||[]).some((r:any)=>r.device_id===id&&r.stage==='received')}
function awaiting(d:any){return d.status==='published'&&!received(d.device_id)}
function age(d:any){const event=(snapshot.value?.events||[]).filter((e:any)=>e.device_id===d.device_id&&e.stage==='published').at(-1);return event?Math.max(0,Math.floor(now.value/1000)-event.created_at):0}
const now=ref(Date.now());
const policy=ref('');
async function upload(event:Event){const file=(event.target as HTMLInputElement).files?.[0];if(!file)return;const body=new FormData();body.append('file',file);try{const r=await fetch('/api/v1/manage/icons',{method:'POST',headers:{Authorization:'Bearer '+props.token},body});const p=await r.json();if(!r.ok)throw Error(p.message);form.icon_url=new URL(p.path,window.location.origin).href}catch(e){error.value=String(e)}}
async function stream(){while(!stopped){controller=new AbortController();try{
 const res=await fetch('/api/v1/manage/events?after='+cursor,{headers:{Authorization:'Bearer '+props.token},signal:controller.signal});if(!res.ok||!res.body)throw Error('事件连接失败');connection.value='实时连接';lastActivity=Date.now();const reader=res.body.getReader(),decoder=new TextDecoder();let buffer='';
 while(!stopped){const {value,done}=await reader.read();if(done)break;lastActivity=Date.now();buffer+=decoder.decode(value,{stream:true});let pos;while((pos=buffer.indexOf('\n\n'))>=0){const frame=buffer.slice(0,pos);buffer=buffer.slice(pos+2);const raw=frame.split('\n').find(s=>s.startsWith('data: '));if(raw){const event=JSON.parse(raw.slice(6));cursor=Math.max(cursor,event.id);if(event.message_id===selected.value)scheduleRefresh()}}}
 }catch(e){if(stopped)return;connection.value='连接恢复中，临时刷新'}
 if(!stopped){connection.value='连接恢复中，临时刷新';await new Promise(r=>setTimeout(r,2500));await refresh()}
}}
onMounted(()=>{try{history.value=JSON.parse(sessionStorage.getItem(storageKey.value)||'[]');selected.value=history.value[0]||''}catch{};refresh();stream();timer=setInterval(()=>{now.value=Date.now();if(connection.value==='实时连接'&&now.value-lastActivity>10000){connection.value='连接恢复中，临时刷新';controller?.abort()}if(connection.value!=='实时连接'&&!document.hidden)refresh()},2000)});
onUnmounted(()=>{stopped=true;controller?.abort();if(timer)clearInterval(timer);if(refreshTimer)clearTimeout(refreshTimer)});
</script>
<template>
<div class="two-columns test-layout">
 <article class="panel"><h2>发送测试通知</h2><p class="muted">测试与正式消息使用相同投递流程。每项能力由实际接收通道决定。</p>
 <form @submit.prevent="send">
  <fieldset><legend>目标设备</legend><label class="check" v-for="d in devices" :key="d.id"><input type="checkbox" v-model="form.device_ids" :value="d.id">{{d.name}} · {{d.id.slice(-6)}}</label></fieldset>
  <fieldset><legend>目标主题</legend><label class="check" v-for="t in topics" :key="t.id"><input type="checkbox" v-model="form.topic_ids" :value="t.id">{{t.name}}</label><small>主题与设备取并集，同一设备只投递一次。</small></fieldset>
  <label>投递策略<select v-model="policy"><option value="">设备默认（FCM/直连/自动）</option><option v-for="p in policies" :value="p.id">{{p.name}}</option></select><small>到“通道与切换”配置备用通道及等待时间。</small></label>
  <label>标题<input v-model="form.title" maxlength="1024"><small>通知的第一行，可为空。</small></label>
  <label>正文<textarea v-model="form.body" required rows="4"></textarea><small>手机收到的内容，最多40000字且不超过65535 UTF-8字节。</small></label>
  <label>内容格式<select v-model="form.content_type"><option value="text">纯文本</option><option value="markdown">Markdown</option><option value="html">HTML（经过清理）</option></select></label>
  <label>优先级<select v-model.number="form.priority"><option v-for="(v,i) in ['最低','低','普通','高','紧急']" :value="i+1">{{i+1}} · {{v}}</option></select><small>影响通知渠道重要性；系统勿扰、用户设置仍然生效。</small></label>
  <label>业务类型<select v-model="form.business_type"><option v-for="(v,k) in business" :value="k">{{v}}</option></select><small>选择业务通知渠道及默认样式。</small></label>
  <label>业务显示名<input v-model="form.business_name" placeholder="例如：数据库备份"><small>在通知中显示的业务名称。</small></label>
  <label>提示音别名<select v-model="form.sound"><option value="default">default · 设备业务默认声音</option><option value="alarm">alarm · 告警</option><option value="soft">soft · 轻柔</option><option value="silent">silent · 静音</option></select><small>选择预定义声音渠道，不覆盖手机系统设置。</small></label>
  <label>右侧业务图标<select v-model="form.icon"><option v-for="(v,k) in business" :value="k">{{v}}</option></select></label>
  <label>自定义图标地址<input v-model="form.icon_url" type="url" placeholder="https://…"><small>HTTPS 图片优先作为大图标；下载失败显示业务图标。</small></label>
  <label>上传自定义图标<input type="file" accept="image/png,image/jpeg" @change="upload"><small>PNG/JPEG，最大900KB。生成的图片链接可公开读取，请勿上传敏感图片。</small></label>
  <label>点击跳转<input v-model="form.click_url" placeholder="https://… 或已允许的应用协议"><small>留空打开消息详情。应用协议需在手机中允许。</small></label>
  <label>通知分组<input v-model="form.group"><small>同组消息归类显示；不改变投递目标。</small></label>
  <label>标签<input v-model="form.tags" placeholder="backup,success"><small>英文逗号分隔，作为业务附加信息。</small></label>
  <label>处理状态回调<input v-model="form.callback_url" type="url" placeholder="https://…"><small>可选，仅允许管理员配置的HTTPS地址。报告服务器处理结果，不能作为手机接收或已读证明。</small></label>
  <label>有效期<select v-model.number="form.ttl"><option :value="0">不设置额外有效期</option><option :value="60">1分钟</option><option :value="3600">1小时</option><option :value="86400">1天</option></select><small>到期后停止尚未完成的投递。</small></label>
  <details><summary>附件与通知操作</summary><label>高级配置（JSON）<textarea v-model="advanced" rows="6" placeholder='{"attachment":{"url":"https://example.com/report.pdf","name":"报告.pdf"},"actions":[{"action":"view","label":"查看","url":"https://example.com"}]}'></textarea></label><small>attachment：HTTPS外部附件；actions：最多3个view打开网页或http发送请求的按钮。仅球球APP和ntfy通道使用；第三方通道能力见使用说明。</small></details>
  <details><summary>请求预览</summary><pre>{{JSON.stringify(payload,null,2)}}</pre></details>
  <button class="primary" :disabled="busy||!form.device_ids.length&&!form.topic_ids.length">{{busy?'正在提交…':'发送测试通知'}}</button>
 </form></article>
 <article class="panel" role="region" aria-label="本次测试投递进度"><h2>本次测试投递进度</h2><p class="muted">{{connection}}</p>
  <label v-if="history.length">查看测试<select v-model="selected" @change="refresh"><option v-for="id in history" :value="id">{{id.slice(0,8)}}</option></select></label>
  <p v-if="submitState" role="status">{{submitState}}</p><p v-if="error" role="alert" class="error">{{error}}</p>
  <p v-if="!selected&&!submitState">尚未发送测试消息</p>
  <template v-if="snapshot&&snapshot.message.message_id===selected"><h3>{{snapshot.message.title||'无标题测试消息'}}</h3><small>{{snapshot.message.message_id}} · {{time(snapshot.message.created_at)}}</small>
   <p role="status">目标 {{stats.total}} · 设备已收到 {{stats.received}} · 发布失败 {{stats.failed}}</p>
   <article v-for="d in snapshot.deliveries" :key="d.id" class="message"><h3>{{d.device_name||d.device_id}}</h3><p>{{received(d.device_id)?'✓ 设备已收到':statusLabel(d.status)}}</p>
    <p v-if="awaiting(d)&&!snapshot.message.delivery_policy" class="muted">{{age(d)>=60?'60秒内未收到回执，接收未知':'等待设备回执 '+age(d)+'秒 / 60秒'}}；发布成功不代表手机接收。</p>
    <p v-for="a in (snapshot.route_attempts||[]).filter((x:any)=>x.delivery_id===d.id)" :key="a.id"><b>{{a.step>=0?'第'+(a.step+1)+'步 · ':''}}{{channelName(a.channel_id)}}</b> · {{labels[a.state]||a.state}} · {{a.detail}}<span v-if="a.state==='waiting_receipt'"> · 剩余 {{Math.max(0,a.deadline-Math.floor(now/1000))}} 秒</span></p>
    <ol class="steps"><li v-for="e in snapshot.events.filter((x:any)=>!x.device_id||x.device_id===d.device_id)" :key="e.id"><b>{{labels[e.stage]||e.stage}}</b> <small>{{time(e.created_at)}}<template v-if="eventChannel(e)"> · {{eventChannel(e)}}</template></small><p v-if="e.detail">{{e.detail}}</p></li></ol>
    <p v-if="!snapshot.receipts.some((r:any)=>r.device_id===d.device_id)">尚无设备回执；旧版 APP 不具备回执能力。</p>
   </article>
  </template>
  <details><summary>状态含义</summary><p>已受理：服务器已保存。ntfy 已接受发布：发布接口返回成功。设备已收到：APP 已保存消息。通知已提交：APP 请求系统发布，不代表人已看到。已打开：用户打开消息。回执超时：接收状态未知。</p></details>
 </article>
</div>
</template>
<script lang="ts">
function statusLabel(s:string){return ({published:'✓ 投递流程已完成，查看通道结果',pending:'等待投递或回执',sending:'投递中',failed:'投递失败'} as Record<string,string>)[s]||s}
</script>
