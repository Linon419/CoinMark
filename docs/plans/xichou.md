核心思路
把问题简化成：在每个时间窗口内，判断买方力量是否异常偏强，然后看这种异常能不能持续。
第一步：定义"异常"
每根1min bar计算：
delta = buy_volume - sell_volume
单根bar的delta没意义，聚合到4h窗口：
delta_4h = sum(delta) over 4h
然后跟历史比，用该币种过去7天的delta_4h分布做基准：
异常 = 当前delta_4h > 历史80分位
为什么用80分位而不是固定值 — 不同币种流动性差异大，RUNE和XRP的绝对delta完全没有可比性，用分位数自适应。
第二步：定义"持续"
连续N个4h窗口被标记为"异常"：
persistence = 最近N个4h窗口中，异常窗口的连续计数
RUNE的案例是7月18-23日，大约5天 = 30个4h窗口。不需要每个窗口都异常，但要求大部分是：
触发条件：最近30个4h窗口中，异常窗口占比 > 60%
第三步：区分大单和散户
同样的delta，100笔小单堆出来的 vs 10笔大单打出来的，含义完全不同。
大单定义：
big_threshold = rolling_percentile(单笔成交量, 95, 过去7天)
额外计算：
big_delta_4h = sum(大单buy_volume - 大单sell_volume) over 4h
如果 big_delta_4h 也异常，信号可信度更高。
第四步：量价背离确认
可选但有用 — Eric在XRP上观察到"买买买但价格没动"：
delta在累积 + 价格横盘或微跌 = 隐蔽吸筹
简单判断：
cum_delta_5d 在上升（正斜率）
price_change_5d 在 ±10% 以内
两者同时满足 = 背离确认。
信号输出
LEVEL 0：无异常
LEVEL 1：delta_4h异常（刚开始出现买入）
LEVEL 2：持续异常 > 2天（吸筹进行中）
LEVEL 3：持续异常 > 2天 + 大单确认（高置信度）
LEVEL 3+背离：持续异常 + 大单 + 价格不动（最强信号，对应XRP案例）
加一个FADING状态：之前达到过LEVEL 2+，但最近异常窗口占比开始下降 → 吸筹可能结束。