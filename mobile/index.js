/**
 * @format
 */

import {AppRegistry} from 'react-native';
import App from './App';
import {name as appName} from './app.json';
import {installCrashGuard} from './src/net/crashGuard';

// Перехват ставим до запуска приложения: ошибка при старте тоже должна быть
// видна текстом, а не выглядеть как «приложение не открывается».
installCrashGuard();

AppRegistry.registerComponent(appName, () => App);
