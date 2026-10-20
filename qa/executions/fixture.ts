import {renderExecutions, type ExecutionProject} from '../../dashboard/execution-view.js';

declare global {
  interface Window {
    renderExecutionFixture: (projects: ExecutionProject[], now: number) => void;
    executionFixtureXss?: number;
  }
}

window.renderExecutionFixture = (projects, now) => {
  document.querySelector('#fixture')!.replaceChildren(renderExecutions(projects, now));
};
