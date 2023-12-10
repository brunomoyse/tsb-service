<?php

use App\Http\Controllers\MollieController;
use App\Http\Controllers\UploadController;
use Illuminate\Support\Facades\Route;

/*
|--------------------------------------------------------------------------
| API Routes
|--------------------------------------------------------------------------
|
| Here is where you can register API routes for your application. These
| routes are loaded by the RouteServiceProvider and all of them will
| be assigned to the "api" middleware group. Make something great!
|
*/

Route::post('/mollie/webhook', [MollieController::class, 'updateOrderStatus']);

Route::post('upload', [UploadController::class, 'uploadImage']);
