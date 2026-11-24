<script setup lang="ts">
import {onMounted,ref} from 'vue';
const props=defineProps<{token:string;code:string}>();
type Row=Record<string,any>;
const info=ref<Row|null>(null),device=ref(''),topics=ref<number[]>([]),busy=ref(false),error=ref(''),notice=ref('');
async function api(path:string,body:unknown){const r=await fetch('/api/v1/manage/subscriptions/'+path,{method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer '+props.token},body:JSON.stringify(body)});const p=await r.json();if(!r.ok)throw Error(p.message||'请求失败');return p}
onMounted(async()=>{try{info.value=await api('preview',{code:props.code})}catch(e){error.value=String(e)}});
async function claim(){busy.value=true;error.value='';try{const result=await api('claim',{code:props.code,device_id:device.value,topic_ids:topics.value});notice.value=result.message+' · UID：'+result.uid}catch(e){error.value=String(e)}finally{busy.value=false}}
</script>
<template><article class="panel"><h2>确认本地设备订阅</h2><p>此二维码由球球服务器签发。选择你已配对的设备及主题后确认；不会创建公开账户或连接微信。</p><p v-if="error" class="error" role="alert">{{error}}</p><p v-if="notice" class="success" role="status">{{notice}}</p><form v-if="info" @submit.prevent="claim"><p>业务参数：{{info.extra}}</p><label>接收设备<select v-model="device" required><option value="" disabled>选择设备</option><option v-for="d in info.devices" :key="d.id" :value="d.id">{{d.name}} · {{d.id.slice(-6)}}</option></select></label><fieldset v-if="info.topics.length"><legend>订阅主题</legend><label v-for="t in info.topics" :key="t.id" class="check"><input type="checkbox" v-model="topics" :value="t.id">{{t.name}}</label></fieldset><p v-if="!topics.length&&!info.device_ids.includes(device)">请勾选主题；直接授权仅允许凭证原本绑定的设备。</p><button class="primary" :disabled="busy||!device||(!topics.length&&!info.device_ids.includes(device))">{{busy?'处理中…':'确认订阅'}}</button></form></article></template>
