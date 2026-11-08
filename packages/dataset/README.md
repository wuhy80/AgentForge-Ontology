# TypeScript DataSet SDK

首批SDK位于本目录，包名 `@agentforge/ontology-dataset`，当前仅在仓库内构建，未发布npm。运行时没有第三方依赖；Node.js 22.12+用于构建和测试，浏览器使用现有Cookie会话及内存CSRF。公共查询语义、权限和能力范围唯一见[DataSet](../../docs/数据查询/DataSet.md)。

```powershell
cd packages/dataset
npm.cmd ci
npm.cmd test
```

构建产物为ESM及TypeScript声明，位于忽略的dist目录。其他项目可显式构建后通过本地包依赖使用；SDK不依赖Ontology工作台、服务端私有源码或数据库。npm发布、CDN分发和其他语言SDK尚未开放。

下面使用已登录的浏览器会话。`session`由应用现有会话接口恢复，不能把CSRF、密码或查询结果写入配置、日志或浏览器持久存储。

```typescript
import { DataSet, createBrowserTransport } from '@agentforge/ontology-dataset';

const transport = createBrowserTransport({
  csrfToken: () => session.csrfToken,
  onUnauthorized: () => showLogin(),
});

const draft = DataSet.from(transport, { category: '业务', schema: '工单' })
  .select('代码', '金额')
  .where({ op: 'gte', field: '金额', value: '9007199254740993.001' })
  .orderBy('代码', 'asc')
  .limit(25);

const bound = await draft.bind();
const first = await bound.execute();
render(first.result);
const second = await first.nextPage();
if (second) render(second.result);
```

`from/fromSpec/select/where/orderBy/limit/aggregate`只构造新的查询对象，不读取业务数据。where追加AND，select和orderBy替换对应配置；派生对象、输入表达式及导出的toQuery快照相互独立。绑定后的投影、条件或排序变更需要重新bind，已绑定源Schema仍保留原ID；改名或同名新建不接管原绑定。

bind调用 `POST /data/query/bind`，由服务端将名称解析成稳定Schema/field ID，并返回安全输出字段；只读取元数据和当前授权，不扫描业务行，也不创建持久定义。它不保证物理表就绪或以后执行成功。execute要求先bind，再调用原 `POST /data/query`；每页继续由后端校验当前权限、定义及来源，绑定计划不是授权令牌。`fromSpec`导入普通无游标QuerySpec后也须绑定，不能用导入标记绕过服务校验。

SDK保持int64、decimal及微秒时间的原始字符串，拒绝数值字段使用JavaScript number/BigInt；NULL和空字符串保留区别。页结果只能作为读取结果，修改它不会回写业务实例，也不会改变后续页的计划或游标。协议错误、未绑定执行、重复游标和未支持组合明确失败，不自动重绑名称、不重试或降级到本地数据。

批量翻页必须显式给出1～100页预算。到预算即停止，本页hasMore仍由服务端返回，不能将截取页数当作完整数据集；取消使用标准AbortSignal，保留AbortError或调用者reason。

```typescript
const controller = new AbortController();
for await (const page of bound.pages({ maxPages: 5, signal: controller.signal })) {
  render(page.result);
}

const totals = await DataSet.from(transport, { category: '业务', schema: '工单' })
  .aggregate({
    groupBy: ['状态'],
    measures: [
      { id: 'mCount', op: 'count', name: '数量' },
      { id: 'mSum', op: 'sum', field: '金额', name: '合计' },
    ],
  })
  .orderBy('mCount', 'desc')
  .bind();
render((await totals.execute()).result);
```

应用退出或替换账号时调用 `transport.invalidate()`，丢弃在途请求，并为新会话重新创建transport；不能把旧transport复用于新账号。

首批只构造单Schema实体及默认平台MySQL聚合，rows/via关系计划明确拒绝。实体映射仍由后端实际能力决定；SDK不额外开放关系聚合、HAVING、单位换算、全文、任意SQL、写入、团队保存、无界all或离线全集计算。三语言Function/Action隔离运行器不因此获得网络或数据SDK。

`createBrowserTransport`仅接受同源路径basePath，默认 `/api/data`；固定POST、Cookie、CSRF、禁止重定向、不缓存。默认20秒超时，可配置1～60000毫秒，响应8MiB预算在流读取时执行；401使该transport及其在途输出失效，应用退出时须显式invalidate。403保留服务端受控错误，不自动登录或更换身份。Node等其他环境可注入 `QueryTransport={bind,query}`，认证仍须遵守公开接口，SDK不代管密码、Cookie容器、OAuth或服务账号。

真实集成使用[跨服务验证](../../tests/integration/dataset_sdk_test.go)，Node子进程经公开TLS、Cookie/CSRF、OPA和独立MySQL测试库运行SDK；只有显式设置 `ONTOLOGY_TEST_DATASET_SDK=1` 才启用，缺node或dist时失败。使用隔离测试库运行父测试 `TestModelEvolutionSessionsAndOPAEndToEnd`；不要把默认开发数据库当作隔离fixture。浏览器transport单测使用模拟fetch，其结果与真实服务集成分别记录在[项目总览](../../docs/项目总览.md#当前实现与验证2026-11-06)。
