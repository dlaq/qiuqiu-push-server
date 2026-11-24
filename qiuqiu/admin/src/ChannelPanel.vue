<script setup lang="ts">
import {ref,computed,onMounted} from 'vue';
type Row=Record<string,any>;
const props=defineProps<{token:string}>();
const emit=defineEmits(['changed']);
const kinds=ref<Record<string,string>>({}),channels=ref<Row[]>([]),policies=ref<Row[]>([]),error=ref(''),notice=ref(''),busy=ref(false);
const name=ref(''),kind=ref('ntfy_fcm'),config=ref<Record<string,string>>({}),policyName=ref(''),mode=ref('fallback'),steps=ref<Row[]>([]);
const editingChannel=ref(''),editingPolicy=ref('');
function editChannel(c:Row){editingChannel.value=c.id;name.value=c.name;kind.value=c.kind;config.value={};notice.value='请重新填写完整配置和凭证；保存后替换原配置。';window.scrollTo({top:0,behavior:'smooth'})}
function editPolicy(p:Row){editingPolicy.value=p.id;policyName.value=p.name;mode.value=p.mode;steps.value=JSON.parse(p.steps);window.scrollTo({top:0,behavior:'smooth'})}
const fields:Record<string,string[]>={email:['host','port','username','password','recipient'],wechat_test:['app_id','secret','template_id','recipient'],wecom_app:['app_id','secret','agent_id','recipient'],wecom_bot:['url'],feishu_app:['app_id','secret','recipient_type','recipient'],feishu_bot:['url','secret'],dingtalk:['url','secret'],onebot:['url','token','recipient_type','recipient'],qq_bot:['app_id','secret','recipient_type','recipient'],telegram:['token','recipient'],discord:['url'],bark:['url','key'],webhook:['url','token']};
const help:Record<string,string>={host:'SMTP服务器，例如 smtp.qq.com',port:'465为TLS，587为STARTTLS；默认587',username:'SMTP登录邮箱，同时作为发件人',password:'SMTP授权码或应用密码',recipient:'接收地址、成员ID、Chat ID、OpenID或群ID',recipient_type:'OneBot/QQ：private或group；飞书：open_id、user_id、union_id、email或chat_id',url:'提供方的完整发送接口或机器人Webhook地址',token:'提供方访问Token，与球球发送凭证不同',secret:'应用Secret或机器人签名密钥',app_id:'应用ID；企业微信填写CorpID',agent_id:'企业微信AgentID',template_id:'微信测试号模板ID',key:'Bark设备Key'};
const selectedFields=computed(()=>fields[kind.value]||[]);
fields.client=['secret'];fields.onebot=['url','token','recipient_type','recipient','mode','allow_private'];
fields.webhook=['url','token','headers','body_template'];help.headers='可选JSON请求头对象，凭证加密保存';help.body_template='可选JSON模板；字符串支持 {{title}}、{{body}}、{{message_id}}、{{click_url}}、{{business_type}}，不执行代码';
help.mode='OneBot：http/正向WS留空；反向WS填写 reverse';help.allow_private='仅管理员可填写 true，允许连接自己的内网机器人服务';
async function api(path:string,method='GET',body?:unknown){const r=await fetch('/api/v1/manage/'+path,{method,headers:{Authorization:'Bearer '+props.token,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});const v=await r.json();if(!r.ok)throw Error(v.message||'操作失败');return v;}
async function load(){const [a,b,c]=await Promise.all([api('capabilities'),api('channels'),api('policies')]);kinds.value=a.channels;channels.value=b.data;policies.value=c.data;}
async function act(fn:()=>Promise<void>){if(busy.value)return;busy.value=true;error.value='';try{await fn();notice.value='已保存';await load();emit('changed')}catch(e){error.value=String(e)}finally{busy.value=false}}
async function createChannel(){await act(async()=>{await api('channels',editingChannel.value?'PATCH':'POST',{id:editingChannel.value,name:name.value,kind:kind.value,config:config.value});config.value={};name.value='';editingChannel.value=''})}
async function createPolicy(){await act(async()=>{await api('policies',editingPolicy.value?'PATCH':'POST',{id:editingPolicy.value,name:policyName.value,mode:mode.value,steps:steps.value});steps.value=[];policyName.value='';editingPolicy.value=''})}
onMounted(()=>{load().catch(e=>error.value=String(e))});
</script>
<template><section><p v-if="error" class="error" role="alert">{{error}}</p><p v-if="notice" role="status">{{notice}}</p><div class="two-columns">
 <article class="panel"><h2>添加投递通道</h2><p>通道决定消息送到哪里；同一种类型可配置多个收件人。凭证加密保存，列表不显示完整凭证。</p><form @submit.prevent="createChannel">
  <label>通道名称<input v-model="name" required placeholder="例如：我的手机FCM、运维企业微信群"></label>
  <label>通道类型<select v-model="kind" :disabled="!!editingChannel" @change="config={}"><option v-for="(label,key) in kinds" :value="key" :disabled="key==='tencent_alarm'">{{label}}</option></select></label>
  <label v-for="field in selectedFields" :key="field">{{field}}<input v-model="config[field]" :type="['password','secret','token','key'].includes(field)?'password':'text'" :autocomplete="['password','secret','token','key'].includes(field)?'new-password':'off'"><small>{{help[field]}}</small></label>
  <p v-if="kind==='ntfy_fcm'||kind==='app_direct'" class="muted">使用消息选择的球球设备，每台设备独立授权；支持手机接收回执。</p>
  <p v-else-if="kind==='store'" class="muted">仅保存消息，不发送到任何客户端。</p>
  <p v-else class="muted">提供方受理成功视为可达；除非有独立回执，不代表手机已收到。优先级、图标、提示音等可能由第三方客户端决定。</p>
  <p v-if="editingChannel">正在编辑通道，请填写完整配置。保存将替换原凭证。</p><button class="primary" :disabled="busy">{{editingChannel?'更新通道':'保存通道'}}</button><button v-if="editingChannel" type="button" class="secondary" @click="editingChannel='';config={};name=''">取消编辑</button>
 </form></article>
 <article class="panel"><h2>创建投递策略</h2><form @submit.prevent="createPolicy">
  <label>策略名称<input v-model="policyName" required placeholder="例如：手机失败后发企业微信"></label>
  <label>投递方式<select v-model="mode"><option value="single">单通道</option><option value="fallback">按顺序故障切换</option><option value="parallel">并行群发</option></select></label>
  <div v-for="(step,i) in steps" :key="i"><label>第{{i+1}}步<select v-model="step.channel_id" required><option value="" disabled>选择通道</option><option v-for="c in channels.filter(c=>!c.disabled)" :value="c.id">{{c.name}} · {{kinds[c.kind]}}</option></select></label><label>等待回执（秒）<input type="number" min="5" max="3600" v-model.number="step.timeout_seconds"></label><button type="button" class="text-button danger" @click="steps.splice(i,1)">移除此步</button></div>
  <button type="button" class="secondary" @click="steps.push({channel_id:'',timeout_seconds:60})">添加通道步骤</button>
  <p class="muted">明确失败立即尝试下一步；有手机回执的通道默认等60秒；无回执的通道受理成功即停止切换。不同第三方客户端可能重复提醒。</p>
  <button class="primary" :disabled="busy||!steps.length">{{editingPolicy?'更新策略':'保存策略'}}</button><button v-if="editingPolicy" type="button" class="secondary" @click="editingPolicy='';steps=[];policyName=''">取消编辑</button>
 </form></article></div>
 <article class="panel"><h2>已配置通道</h2><div v-for="c in channels" :key="c.id" class="binding"><span>{{c.name}} · {{kinds[c.kind]}} · {{c.disabled?'已禁用':'已启用，连通性需测试'}}</span><button v-if="!c.disabled" class="text-button" @click="editChannel(c)">编辑配置</button><button v-if="!c.disabled" class="text-button danger" @click="act(async()=>{await api('channels?id='+c.id,'DELETE')})">禁用</button></div></article>
 <article class="panel"><h2>已保存策略</h2><div v-for="p in policies" :key="p.id" class="message"><h3>{{p.name}}</h3><p>{{p.mode==='fallback'?'顺序切换':p.mode==='single'?'单通道':'并行群发'}}</p><ol><li v-for="step in JSON.parse(p.steps)">{{channels.find(c=>c.id===step.channel_id)?.name||'不可用通道'}} · {{step.timeout_seconds}}秒</li></ol><code>{{p.id}}</code><button class="text-button" @click="editPolicy(p)">编辑策略</button></div></article>
</section></template>
