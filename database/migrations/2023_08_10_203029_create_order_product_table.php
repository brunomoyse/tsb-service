<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::create('order_product', function (Blueprint $table) {
            $table->uuid('order_id');
            $table->foreign('order_id')->references('id')->on('orders')->onDelete('cascade');
            $table->uuid('product_id');
            $table->foreign('product_id')->references('id')->on('products')->onDelete('cascade');
            $table->unique(['product_id', 'order_id']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('order_product');
    }
};
