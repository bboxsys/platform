// The ECharts build the assistant's charts use: the core plus the series
// and components the chart guidance leads the model to. Static named
// imports so the bundler keeps only these; AssistantChart loads this
// module on demand, so a page carries none of it until a chart appears.
import * as core from 'echarts/core'
import { BarChart, LineChart, PieChart, ScatterChart } from 'echarts/charts'
import {
  DatasetComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TitleComponent,
  TooltipComponent,
  TransformComponent,
} from 'echarts/components'
// SVG rather than canvas: the chart is small, text stays crisp, and the
// download button exports the drawing itself through getDataURL.
import { SVGRenderer } from 'echarts/renderers'

core.use([
  BarChart, LineChart, PieChart, ScatterChart,
  DatasetComponent, GridComponent, LegendComponent, MarkLineComponent, TitleComponent, TooltipComponent, TransformComponent,
  SVGRenderer,
])

export const { init, registerTheme } = core
